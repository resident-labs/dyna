package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
)

// ProviderKeysResult carries provider names only. No call returns a key.
type ProviderKeysResult struct {
	// Providers hold an API key in the harness's private home.
	Providers []native.ProviderKey `json:"providers"`
	// Choices are the providers the installed harness can take a key for. Set by "choices".
	Choices []native.ProviderKey `json:"choices,omitempty"`
}

var (
	providerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// Pi's provider catalog ships in its npm package, one JSON file per provider.
	piCatalog = filepath.Join("lib", "node_modules", "@earendil-works", "pi-coding-agent", "node_modules", "@earendil-works", "pi-ai", "dist", "providers", "data")
)

const maxProviderKey = 8 << 10

var errProviderKeys = errors.New("This harness does not take provider API keys.")

// HarnessProviderKeys is the one engine call for API-key providers of Pi and
// OpenCode. action is "choices", "list", "add" or "remove". The key is only
// read for "add" and goes straight into the harness's private auth store.
func (a *Runtime) HarnessProviderKeys(ctx context.Context, harness, action, provider, key string) (ProviderKeysResult, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil || (h != "pi" && h != "opencode") {
		return ProviderKeysResult{}, errProviderKeys
	}
	if (action == "add" || action == "remove") && !providerID.MatchString(provider) {
		return ProviderKeysResult{}, errors.New("Choose a provider.")
	}
	if action == "add" && (key == "" || len(key) > maxProviderKey || strings.ContainsAny(key, "\r\n\x00")) {
		return ProviderKeysResult{}, errors.New("Enter the API key on one line.")
	}
	home, err := a.Accounts.HarnessHome(h)
	if err != nil {
		return ProviderKeysResult{}, err
	}
	var result ProviderKeysResult
	if h == "pi" {
		result, err = a.piProviderKeysEdit(ctx, home, action, provider, key)
	} else {
		result, err = a.openCodeProviderKeys(ctx, home, action, provider, key)
	}
	if err != nil {
		return ProviderKeysResult{}, err
	}
	if result.Providers == nil {
		result.Providers = []native.ProviderKey{}
	}
	return result, nil
}

// Pi ignores a lock directory whose mtime is older than its stale limit: 10
// seconds for synchronous operations and 30 for asynchronous ones. Dyna breaks
// a lock only past the longer limit. While it holds its own, it renews the
// mtime every piBeat, as proper-lockfile does, so a slow write never looks stale.
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

func (a *Runtime) piProviderKeys(ctx context.Context, home, action, provider, key string) (ProviderKeysResult, error) {
	path := filepath.Join(home, "auth.json")
	if action == "choices" {
		choices, err := a.piChoices()
		return ProviderKeysResult{Choices: choices}, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return ProviderKeysResult{}, errors.New("Pi's private home is not available.")
	}
	unlock, err := lockPiAuth(ctx, path)
	if err != nil {
		return ProviderKeysResult{}, err
	}
	defer unlock.release()
	if err := checkPrivateAccountFile(path, true); err != nil {
		return ProviderKeysResult{}, err
	}
	entries := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err == nil {
		// A JSON null decodes without error but leaves a nil map.
		if len(raw) > 4<<20 || json.Unmarshal(raw, &entries) != nil || entries == nil {
			return ProviderKeysResult{}, errors.New("Pi's login file is not valid JSON.")
		}
	} else if !os.IsNotExist(err) {
		return ProviderKeysResult{}, errors.New("Pi's login file could not be read.")
	}
	switch action {
	case "list":
	case "add":
		entries[provider], _ = json.Marshal(map[string]string{"type": "api_key", "key": key})
	case "remove":
		// An entry that is already gone needs no key check: the saved snapshot
		// can lack a key the runtime home still lists. A present non-key entry
		// (an OAuth login) stays protected.
		if raw := entries[provider]; raw != nil && !piKeyEntry(raw) {
			return ProviderKeysResult{}, errors.New("That provider has no API key.")
		}
		delete(entries, provider)
	default:
		return ProviderKeysResult{}, errors.New("Unknown provider action.")
	}
	if action != "list" {
		// Checked again just before the rename: a lock lost during the write keeps Pi's file.
		lost := func() error {
			if unlock.held() {
				return nil
			}
			return errors.New("Pi's login file lock was lost. Try again.")
		}
		if err := writePrivateAccountJSONChecked(path, entries, lost); err != nil {
			return ProviderKeysResult{}, err
		}
	}
	var result ProviderKeysResult
	for id, entry := range entries {
		if piKeyEntry(entry) {
			result.Providers = append(result.Providers, native.ProviderKey{ID: id, Name: id})
		}
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	return result, nil
}

// piKeysMu serializes Dyna's own Pi key edits, so the saved account and the
// runtime home never take two concurrent edits in different orders.
var piKeysMu sync.Mutex

// piProviderKeysEdit applies an add or remove to the active Pi account's own
// login file and to the runtime home as one operation: a failure in the runtime
// write restores the account file. Pi needs an active account to start a turn,
// so the first key creates and activates one.
func (a *Runtime) piProviderKeysEdit(ctx context.Context, home, action, provider, key string) (ProviderKeysResult, error) {
	if action != "add" && action != "remove" {
		return a.piProviderKeys(ctx, home, action, provider, key)
	}
	piKeysMu.Lock()
	defer piKeysMu.Unlock()
	// The first lock of an account switch or native login. Holding it keeps the
	// active account fixed through the edit without waiting for running turns.
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return ProviderKeysResult{}, err
	}
	gate, err := acquireAccountLock(ctx, filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		return ProviderKeysResult{}, errors.New("Pi's account is changing. Try again in a moment.")
	}
	defer releaseAccountLock(gate)
	bundle, err := a.Bundles.Resolve("pi", "")
	if err != nil {
		return ProviderKeysResult{}, errors.New("Install Dyna's copy of Pi first.")
	}
	account, err := a.Accounts.Active("pi")
	created := err != nil
	if created {
		if account, err = a.Accounts.Create("pi", "API keys"); err != nil {
			return ProviderKeysResult{}, err
		}
	}
	file := filepath.Join(account.Home, "auth.json")
	before, readErr := os.ReadFile(file)
	// restore puts the account file back and reports a failure to do so.
	restore := func(cause error) error {
		err := restorePiAuth(file, before, readErr == nil, nil)
		if err == nil {
			_, err = a.Accounts.Capture(context.Background(), account.ID, bundle.Executable)
		}
		if err != nil {
			return errors.Join(cause, errors.New("Pi's saved login could not be restored. Add or remove the key again."))
		}
		return cause
	}
	if _, err = a.piProviderKeys(ctx, account.Home, action, provider, key); err != nil {
		return ProviderKeysResult{}, err
	}
	if _, err = a.Accounts.Capture(ctx, account.ID, bundle.Executable); err != nil {
		return ProviderKeysResult{}, restore(errors.New("Pi's login could not be recorded."))
	}
	result, err := a.piProviderKeys(ctx, home, action, provider, key)
	if err != nil {
		return ProviderKeysResult{}, restore(err)
	}
	if created {
		if err = a.Accounts.setActive("pi", account.ID); err != nil {
			return ProviderKeysResult{}, err
		}
	}
	return result, nil
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

// piKeyEntry is true for an auth.json entry that holds an API key. OAuth logins stay out of the list.
func piKeyEntry(raw json.RawMessage) bool {
	var entry struct{ Type string }
	return json.Unmarshal(raw, &entry) == nil && entry.Type == "api_key"
}

// piChoices reads Pi's own provider catalog from the installed package.
func (a *Runtime) piChoices() ([]native.ProviderKey, error) {
	bundle, err := a.Bundles.Resolve("pi", "")
	if err != nil {
		return nil, errors.New("Install Dyna's copy of Pi first.")
	}
	entries, err := os.ReadDir(filepath.Join(bundle.Directory, piCatalog))
	if err != nil {
		return nil, errors.New("Pi's provider list could not be read.")
	}
	choices := []native.ProviderKey{}
	for _, entry := range entries {
		if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok && providerID.MatchString(id) {
			choices = append(choices, native.ProviderKey{ID: id, Name: id})
		}
	}
	return choices, nil
}

func (a *Runtime) openCodeProviderKeys(ctx context.Context, home, action, provider, key string) (ProviderKeysResult, error) {
	bundle, err := a.Bundles.Resolve("opencode", "")
	if err != nil {
		return ProviderKeysResult{}, errors.New("Install Dyna's copy of OpenCode first.")
	}
	// The commands run inside the private home, so it must exist before a first login.
	if err = harnesshome.Ensure(home); err != nil {
		return ProviderKeysResult{}, err
	}
	manager, _, err := a.Accounts.scopedManager("opencode", home, bundle.Executable)
	if err != nil {
		return ProviderKeysResult{}, err
	}
	if checked, ok := manager.(checkedHomeManager); ok {
		manager = checked.Manager
	}
	keys, ok := native.OpenCodeKeysOf(manager)
	if !ok {
		return ProviderKeysResult{}, errProviderKeys
	}
	var result ProviderKeysResult
	switch action {
	case "choices":
		result.Choices, err = keys.Choices(ctx)
		return result, err
	case "list":
	case "add":
		err = keys.Add(ctx, provider, key)
	case "remove":
		err = keys.Remove(ctx, provider)
	default:
		err = errors.New("Unknown provider action.")
	}
	if err != nil {
		return ProviderKeysResult{}, err
	}
	result.Providers, err = keys.List(ctx)
	return result, err
}
