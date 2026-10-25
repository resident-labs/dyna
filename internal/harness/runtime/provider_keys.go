package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
)

// errProviderKeys is the refusal of a harness that keeps no API keys.
var errProviderKeys = errors.New("This harness does not take provider API keys.")

const maxProviderKey = 8 << 10

// piKeysMu serializes Dyna's own Pi key edits, so two edits of one account
// never take two concurrent writes in different orders.
var piKeysMu sync.Mutex

const piAuthStale = 30 * time.Second

var (
	piNow  = time.Now
	piBeat = 2 * time.Second
	// piBeatDone runs after each renewal; tests wait on it.
	piBeatDone func()
)

// piLock is Pi's own auth lock (proper-lockfile: an exclusive "<file>.lock"
// directory), so an edit here cannot interleave with Pi's login or token refresh.
type piLock struct {
	path string
	mu   sync.Mutex
	lost bool // a renewal failed or the lock changed hands
	info os.FileInfo
	stop chan struct{}
	done chan struct{}
}

func lockPiAuth(ctx context.Context, path string) (*piLock, error) {
	lock := path + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.New("Pi is using its login file. Try again in a moment.")
		}
		err := os.Mkdir(lock, 0700)
		if err == nil {
			info, statErr := os.Stat(lock)
			if statErr != nil {
				return nil, errors.New("Pi's login file could not be locked.")
			}
			l := &piLock{path: lock, info: info, stop: make(chan struct{}), done: make(chan struct{})}
			go l.renew()
			return l, nil
		}
		if !os.IsExist(err) {
			return nil, errors.New("Pi's login file could not be locked.")
		}
		if info, statErr := os.Stat(lock); statErr == nil && piNow().Sub(info.ModTime()) > piAuthStale {
			if rmErr := os.Remove(lock); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, errors.New("Pi's old lock on its login file could not be removed.")
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (l *piLock) renew() {
	defer close(l.done)
	ticker := time.NewTicker(piBeat)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.mu.Lock()
			l.lost = l.lost || !l.renewOnce()
			l.mu.Unlock()
			if piBeatDone != nil {
				piBeatDone()
			}
		}
	}
}

func (l *piLock) renewOnce() bool {
	if !l.owned() {
		return false
	}
	now := piNow()
	if os.Chtimes(l.path, now, now) != nil {
		return false
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return false
	}
	l.info = info
	return true
}

// owned reports whether the lock is still the directory Dyna made and renewed.
// A directory Pi replaced or touched is not ours.
func (l *piLock) owned() bool {
	info, err := os.Stat(l.path)
	return err == nil && os.SameFile(info, l.info) && info.ModTime().Equal(l.info.ModTime())
}

func (l *piLock) held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.lost && l.owned()
}

// release stops the renewal and removes the lock only while it is still ours.
func (l *piLock) release() {
	close(l.stop)
	<-l.done
	if l.held() {
		os.Remove(l.path)
	}
}

// piAuthEdit changes the provider entries of one auth.json. A nil edit only reads.
type piAuthEdit func(entries map[string]json.RawMessage)

// piAPIKeyEdit is the edit that writes one provider's API key into an
// account's auth.json.
func piAPIKeyEdit(provider, key string) piAuthEdit {
	entry, _ := json.Marshal(map[string]string{"type": "api_key", "key": key})
	return func(entries map[string]json.RawMessage) { entries[provider] = entry }
}

// piEditAuth applies edit to home's auth.json under Pi's own lock and returns
// the entries as they stand afterwards.
func piEditAuth(ctx context.Context, home string, edit piAuthEdit) (map[string]json.RawMessage, error) {
	path := filepath.Join(home, "auth.json")
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, errors.New("Pi's private home is not available.")
	}
	unlock, err := lockPiAuth(ctx, path)
	if err != nil {
		return nil, err
	}
	defer unlock.release()
	if err := checkPrivateAccountFile(path, true); err != nil {
		return nil, err
	}
	entries := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err == nil {
		// A JSON null decodes without error but leaves a nil map.
		if len(raw) > 4<<20 || json.Unmarshal(raw, &entries) != nil || entries == nil {
			return nil, errors.New("Pi's login file is not valid JSON.")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("Pi's login file could not be read.")
	}
	if edit == nil {
		return entries, nil
	}
	edit(entries)
	// Checked again just before the rename: a lock lost during the write keeps Pi's file.
	lost := func() error {
		if unlock.held() {
			return nil
		}
		return errors.New("Pi's login file lock was lost. Try again.")
	}
	if err := writePrivateAccountJSONChecked(path, entries, lost); err != nil {
		return nil, err
	}
	return entries, nil
}

// lockPiAccounts holds the first lock of an account switch or native login, so
// the active account stays fixed through a login edit without waiting for
// running turns. The caller releases it when the edit and its record are done.
func (a *Runtime) lockPiAccounts(ctx context.Context) (func(), error) {
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return nil, err
	}
	gate, err := acquireAccountLock(ctx, filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, errors.New("Pi's account is changing. Try again in a moment.")
	}
	return func() { releaseAccountLock(gate) }, nil
}

// writePiAccountLogin applies edit to one Pi account's login, records it, and
// makes the account active for its provider. The caller holds the Pi account
// locks, so no switch moves the account during the write.
func (a *Runtime) writePiAccountLogin(ctx context.Context, account NativeAccount, edit piAuthEdit) error {
	file := filepath.Join(account.Home, "auth.json")
	before, readErr := os.ReadFile(file)
	// restore puts the account file back and reports a failure to do so.
	restore := func(cause error) error {
		err := restorePiAuth(file, before, readErr == nil, nil)
		if err == nil {
			_, err = a.Accounts.Capture(context.Background(), account.ID, "")
		}
		if err != nil {
			return errors.Join(cause, errors.New("Pi's saved login could not be restored. Add or remove the key again."))
		}
		return cause
	}
	if _, err := piEditAuth(ctx, account.Home, edit); err != nil {
		return err
	}
	captured, err := a.Accounts.Capture(ctx, account.ID, "")
	if err != nil {
		if errors.Is(err, native.ErrNotSignedIn) {
			// The last entry is gone, so the account is signed out now.
			return nil
		}
		return restore(errors.New("Pi's login could not be recorded."))
	}
	return a.Accounts.setActiveAccount(captured)
}

// restorePiAuth puts back the file's earlier content, or removes it when it did
// not exist. It takes Pi's lock and keeps it to the commit, like any other edit.
// beforeCommit runs just before the commit; tests use it.
func restorePiAuth(file string, before []byte, existed bool, beforeCommit func()) error {
	unlock, err := lockPiAuth(context.Background(), file)
	if err != nil {
		return err
	}
	defer unlock.release()
	check := func() error {
		if beforeCommit != nil {
			beforeCommit()
		}
		if unlock.held() {
			return nil
		}
		return errors.New("Pi's login file lock was lost.")
	}
	if existed {
		if err = writePrivateAccountJSONChecked(file, json.RawMessage(before), check); err != nil {
			// The checked writer drops its temporary file on a refused commit, so
			// keep the saved credentials in a private recovery file.
			if keepPiRecovery(file, before) != nil {
				return errors.Join(err, errors.New("The saved login could not be kept."))
			}
		}
		return err
	}
	if err = check(); err != nil {
		return err
	}
	if err = os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// keepPiRecovery saves data in a new private file next to file. The name is
// unique and created exclusively, so it never replaces earlier recovery data or
// follows a link, and the mode is 0600.
func keepPiRecovery(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".recovery-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}
