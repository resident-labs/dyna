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
	"dyna-agent/internal/account/store"
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
	var result ProviderKeysResult
	if h == "pi" {
		result, err = a.piProviderKeysEdit(ctx, action, provider, key)
	} else {
		var home string
		if home, err = a.Accounts.HarnessHome(h); err != nil {
			return ProviderKeysResult{}, err
		}
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

// piAuthEdit changes the provider entries of one auth.json. A nil edit only reads.
type piAuthEdit func(entries map[string]json.RawMessage)

// piActionEdit is the edit for an "add" or "remove" action; any other action reads.
func piActionEdit(action, provider, key string) (piAuthEdit, error) {
	switch action {
	case "list":
		return nil, nil
	case "add":
		entry, _ := json.Marshal(map[string]string{"type": "api_key", "key": key})
		return func(entries map[string]json.RawMessage) { entries[provider] = entry }, nil
	case "remove":
		// An entry that is already gone is fine: the saved snapshot can lack a
		// provider the runtime home still lists.
		return func(entries map[string]json.RawMessage) { delete(entries, provider) }, nil
	}
	return nil, errors.New("Unknown provider action.")
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

// piProviderKeys answers choices and list for Pi. The list reads the accounts,
// because each account holds the login of its own provider.
func (a *Runtime) piProviderKeys(ctx context.Context, action, provider, key string) (ProviderKeysResult, error) {
	if action == "choices" {
		choices, err := a.piChoices()
		return ProviderKeysResult{Choices: choices}, err
	}
	if _, err := piActionEdit(action, provider, key); err != nil {
		return ProviderKeysResult{}, err
	}
	return a.piProviderKeysList(ctx)
}

// piProviderKeysList names the provider entries of every Pi account.
func (a *Runtime) piProviderKeysList(ctx context.Context) (ProviderKeysResult, error) {
	accounts, err := a.Accounts.List()
	if err != nil {
		return ProviderKeysResult{}, err
	}
	entries := map[string]json.RawMessage{}
	for _, n := range accounts {
		if n.Harness != "pi" || n.Provider == "" {
			continue
		}
		for id, entry := range piSavedEntries(ctx, n.Home) {
			entries[id] = entry
		}
	}
	return piProviderList(entries), nil
}

// piProviderList names every provider Pi can use, by API key or by sign-in.
func piProviderList(entries map[string]json.RawMessage) ProviderKeysResult {
	var result ProviderKeysResult
	for id, entry := range entries {
		var kind struct{ Type string }
		if json.Unmarshal(entry, &kind) == nil && (kind.Type == "api_key" || kind.Type == "oauth") {
			result.Providers = append(result.Providers, native.ProviderKey{ID: id, Name: id, Auth: kind.Type})
		}
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	return result
}

// piKeysMu serializes Dyna's own Pi key edits, so two edits of one account
// never take two concurrent writes in different orders.
var piKeysMu sync.Mutex

// piProviderKeysEdit applies an add or remove to the login of the account of
// that provider, creating the account for a new key. A failed write restores the
// account's earlier login. Pi reads one provider per account, so the edit never
// touches another provider's credentials.
func (a *Runtime) piProviderKeysEdit(ctx context.Context, action, provider, key string) (ProviderKeysResult, error) {
	if action != "add" && action != "remove" {
		return a.piProviderKeys(ctx, action, provider, key)
	}
	edit, err := piActionEdit(action, provider, key)
	if err != nil {
		return ProviderKeysResult{}, err
	}
	if err = a.editPiProviderAccount(ctx, piProvider(provider), edit, action == "add"); err != nil {
		return ProviderKeysResult{}, err
	}
	return a.piProviderKeysList(ctx)
}

// editPiProviderAccount applies edit to the login of the account of provider,
// inside the Pi account gate so no switch moves the account during the write.
// With create it makes the account first, so a pasted key gets its own account.
func (a *Runtime) editPiProviderAccount(ctx context.Context, provider store.Provider, edit piAuthEdit, create bool) error {
	piKeysMu.Lock()
	defer piKeysMu.Unlock()
	// The first lock of an account switch or native login. Holding it keeps the
	// active account fixed through the edit without waiting for running turns.
	dir, err := a.Accounts.harnessDirectory("pi")
	if err != nil {
		return err
	}
	gate, err := acquireAccountLock(ctx, filepath.Join(dir, ".switch.lock"), syscall.LOCK_EX)
	if err != nil {
		return errors.New("Pi's account is changing. Try again in a moment.")
	}
	defer releaseAccountLock(gate)
	account, found, err := a.piAccountForProvider(ctx, provider)
	if err != nil {
		return err
	}
	if !found {
		if !create {
			return nil
		}
		if account, err = a.Accounts.CreateProvider("pi", provider, store.KindAPIKey, string(provider)+" key"); err != nil {
			return err
		}
	}
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
	if _, err = piEditAuth(ctx, account.Home, edit); err != nil {
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
