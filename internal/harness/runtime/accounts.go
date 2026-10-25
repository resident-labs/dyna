package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	harnesshome "dyna-agent/internal/harness/home"
)

type NativeAccount struct {
	proof     *store.PrivateProof
	prepared  *preparedAccount
	ID        string         `json:"id"`
	Harness   string         `json:"harness"`
	Provider  store.Provider `json:"provider,omitempty"`
	Kind      store.Kind     `json:"kind,omitempty"`
	Label     string         `json:"label"`
	Home      string         `json:"home"`
	Identity  string         `json:"identity"`
	Status    string         `json:"status"`
	Active    bool           `json:"active"`
	ExpiresAt *time.Time     `json:"expiresAt,omitempty"`
	// Repos lists the repositories this account is limited to; empty means any.
	Repos []string `json:"repos"`
}

type Accounts struct {
	Root string
	// NativeManager permits offline adapter injection and additional account backends.
	NativeManager func(string, native.Host) (native.Manager, bool)
	// Quiet makes its registry writes publish no account change. See store.Store.
	Quiet bool
}

func NewAccounts() *Accounts { return &Accounts{Root: filepath.Join(runtimeDataDir(), "accounts")} }

func canonicalAccountHarness(h string) (string, error) {
	h = canonicalRuntimeID(h)
	switch h {
	case "claude-code", "codex", "opencode", "muse", "grok", "pi", "kimi-code":
		return h, nil
	}
	return "", native.ErrUnsupported
}

// These native backends need a drained harness gate for interactive login.
func sharedServiceAccount(h string) bool {
	return h == "opencode" || h == "muse" || h == "kimi-code"
}

var errAccountPath = errors.New("The private account path is unsafe.")
var errAccountMissing = errors.New("The private account does not exist.")
var errAccountHarness = errors.New("The account names a harness this release does not run.")

// AccountProblemReason is why one saved row could not be listed.
type AccountProblemReason string

const (
	// AccountUnknownHarness names a row of a harness this release does not run.
	AccountUnknownHarness AccountProblemReason = "unknown-harness"
	// AccountUnsafeHome names a row whose saved home is unsafe or gone.
	AccountUnsafeHome AccountProblemReason = "unsafe-home"
	// AccountMissingHome names a row that names no saved home.
	AccountMissingHome AccountProblemReason = "missing-home"
	// AccountUnreadable names a row that could not be read for another reason.
	AccountUnreadable AccountProblemReason = "unreadable"
)

// AccountProblem is one saved account row that the list skipped. It is a result
// about that row, never a reason to fail the whole list: one bad row must not
// hide every other account.
type AccountProblem struct {
	ID      string               `json:"id"`
	Harness string               `json:"harness,omitempty"`
	Label   string               `json:"label,omitempty"`
	Reason  AccountProblemReason `json:"reason"`
}

// safeAccountPath rejects symlinks in every existing path component.
func safeAccountPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errAccountPath
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errAccountPath
		}
	}
	return nil
}

func (a *Accounts) transaction(fn func(*store.State) error, write bool) error {
	if err := safeAccountPath(a.Root); err != nil {
		return err
	}
	if err := safeAccountPath(filepath.Join(a.Root, "accounts.lock")); err != nil {
		return err
	}
	s := &store.Store{Root: a.Root, Quiet: a.Quiet}
	if !write {
		state, err := s.Read()
		if err != nil {
			return err
		}
		return fn(&state)
	}
	return s.Transaction(func(state *store.State, checkpoint func() error) error {
		if err := fn(state); err != nil {
			return err
		}
		if write {
			return checkpoint()
		}
		return nil
	})
}

func nativeEntryFor(state *store.State, h, integration, identity string) *store.Entry {
	for i := range state.Entries {
		e := &state.Entries[i]
		if e.NativeHome != "" && e.Harness == h && e.Identity == identity && e.Integration == integration {
			return e
		}
	}
	return nil
}

func (a *Accounts) native(e *store.Entry) (NativeAccount, error) {
	if e == nil || e.NativeHome == "" {
		return NativeAccount{}, errAccountMissing
	}
	h, err := canonicalAccountHarness(e.Harness)
	if err != nil || h != e.Harness {
		return NativeAccount{}, errAccountHarness
	}
	if len(e.ID) != 32 {
		return NativeAccount{}, errAccountPath
	}
	if _, err := hex.DecodeString(e.ID); err != nil {
		return NativeAccount{}, errAccountPath
	}
	home := filepath.Join(a.Root, h, e.ID)
	if e.NativeHome != home && !equivalentAccountHome(a.Root, h, e.ID, e.NativeHome) {
		return NativeAccount{}, errAccountPath
	}
	if err := safeAccountPath(home); err != nil {
		return NativeAccount{}, err
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() {
		return NativeAccount{}, errAccountPath
	}
	status := "ready"
	if e.NeedsLogin || e.Identity == "" || requirePrivateAccount(h) && !privateProofHolds(e.Identity, e.PrivateProof) {
		status = "not signed in"
	}
	var expiresAt *time.Time
	if e.Identity != "" {
		if c, err := readAccountLogin(home); err == nil && c.Identity == e.Identity && c.Integration == e.Integration {
			expiry := native.CredentialExpiry(c.Credential)
			if !expiry.IsZero() {
				expiresAt = &expiry
				if m, ok := native.For(h, native.Host{}); ok && !native.SupportsRefresh(m, c) && native.CredentialExpired(c, time.Now()) {
					status = "not signed in"
				}
			}
		}
	}
	active, err := a.activeForEntry(e)
	if err != nil {
		return NativeAccount{}, err
	}
	if requirePrivateAccount(h) && status == "ready" {
		if _, err := readAccountLogin(home); err != nil {
			status = "not signed in"
		}
		if active == e.ID {
			scope := filepath.Join(a.Root, h, ".runtime")
			paths := privateAccountFiles(h, scope)
			if len(paths) == 0 {
				status = "not signed in"
			} else if _, err := os.Lstat(paths[0]); err != nil || checkPrivateAccountHome(h, scope, false) != nil {
				status = "not signed in"
			}
		}
	}
	return NativeAccount{ID: e.ID, Harness: h, Provider: e.Provider, Kind: e.Kind, Label: e.Label, Home: home, Identity: e.Identity, Status: status, Active: active == e.ID, ExpiresAt: expiresAt, proof: e.PrivateProof}, nil
}

// activeForEntry returns the account the registry marks active for the entry.
// Every harness keeps one selection per provider, so two Pi accounts of
// different providers are active together and a Pi account never marks the
// account Claude Code uses for the same provider.
func (a *Accounts) activeForEntry(e *store.Entry) (string, error) {
	return (&store.Store{Root: a.Root}).Active(SelectionProvider(e.Harness, e.Provider), e.Harness, "")
}

func (a *Accounts) Create(harness, label string) (result NativeAccount, err error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return result, err
	}
	provider, kind := store.ProviderFor(h, "")
	return a.createProvider(h, provider, kind, label)
}

// CreateProvider makes an account of one provider in its own home. A harness
// that runs several providers, such as Pi, gives each provider its own account.
func (a *Accounts) CreateProvider(harness string, provider store.Provider, kind store.Kind, label string) (NativeAccount, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return NativeAccount{}, err
	}
	return a.createProvider(h, provider, kind, label)
}

func (a *Accounts) createProvider(h string, provider store.Provider, kind store.Kind, label string) (result NativeAccount, err error) {
	err = a.transaction(func(state *store.State) error {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		key := hex.EncodeToString(id)
		dir := filepath.Join(a.Root, h)
		if err := safeAccountPath(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
		home := filepath.Join(dir, key)
		if err := os.Mkdir(home, 0700); err != nil {
			return err
		}
		now := time.Now().UTC()
		e := store.Entry{Metadata: store.Metadata{ID: key, Provider: provider, Kind: kind, Harness: h, Label: label, NativeHome: home, NeedsLogin: true, CreatedAt: now, UpdatedAt: now}}
		state.Entries = append(state.Entries, e)
		result, err = a.native(&e)
		return err
	}, true)
	return
}

func (a *Accounts) Find(id string) (result NativeAccount, err error) {
	err = a.transaction(func(state *store.State) error {
		if e := state.Find(id); e != nil && retiredHarness(e.Harness) {
			return errAccountMissing
		}
		result, err = a.native(state.Find(id))
		return err
	}, false)
	return
}

// List returns every account this release can run. A saved row it cannot read is
// left out; ListWithProblems names it.
func (a *Accounts) List() ([]NativeAccount, error) {
	result, _, err := a.ListWithProblems()
	return result, err
}

// ListWithProblems returns the accounts and names each saved row it skipped, so
// one bad row never fails the list or hides the other accounts.
func (a *Accounts) ListWithProblems() (result []NativeAccount, problems []AccountProblem, err error) {
	result = []NativeAccount{}
	problems = []AccountProblem{}
	rules, err := a.rules()
	if err != nil {
		return result, problems, err
	}
	err = a.transaction(func(state *store.State) error {
		for i := range state.Entries {
			e := &state.Entries[i]
			if e.NativeHome == "" || retiredHarness(e.Harness) {
				continue
			}
			n, e2 := a.native(e)
			if e2 != nil {
				problems = append(problems, accountProblem(*e, e2))
				continue
			}
			n.Repos = rules[n.ID]
			result = append(result, n)
		}
		return nil
	}, false)
	return result, problems, err
}

// accountProblem names why one saved row was skipped.
func accountProblem(e store.Entry, err error) AccountProblem {
	reason := AccountUnreadable
	switch {
	case errors.Is(err, errAccountHarness):
		reason = AccountUnknownHarness
	case errors.Is(err, errAccountMissing):
		reason = AccountMissingHome
	case errors.Is(err, errAccountPath):
		reason = AccountUnsafeHome
	}
	return AccountProblem{ID: e.ID, Harness: e.Harness, Label: e.Label, Reason: reason}
}

// privateAccountManager disallows copying or refreshing saved grants.
type privateAccountManager struct {
	native.Manager
	home       string
	nativeRoot bool
}

func (m privateAccountManager) Capture(ctx context.Context, root string) ([]native.Captured, error) {
	if root != "" && root != m.home {
		return nil, errAccountPath
	}
	if err := safeAccountPath(m.home); err != nil {
		return nil, err
	}
	if err := checkAccountFiles(m.Harness(), m.home, false); err != nil {
		return nil, err
	}
	if m.nativeRoot {
		return m.Manager.Capture(ctx, "")
	}
	return m.Manager.Capture(ctx, m.home)
}
func (m privateAccountManager) LoginCommand(string, string) ([]string, []string, error) {
	return nil, nil, native.ErrUnsupported
}
func (m privateAccountManager) Activate(context.Context, string, store.Credential) ([]native.Captured, error) {
	return nil, native.ErrUnsupported
}
func (m privateAccountManager) Refresh(context.Context, native.Captured) (native.Captured, error) {
	return native.Captured{}, native.ErrUnsupported
}
func (m privateAccountManager) Caps() native.Caps {
	c := m.Manager.Caps()
	c.Activate = false
	c.Refresh = false
	c.IsolatedLogin = false
	return c
}

// HarnessHome returns the shared native history and state directory.
func (a *Accounts) HarnessHome(harness string) (string, error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return "", err
	}
	home := filepath.Join(a.Root, h, ".runtime")
	if err = safeAccountPath(home); err != nil {
		return "", err
	}
	return home, nil
}

func (a *Accounts) scopedManager(h, home, executable string) (native.Manager, map[string]string, error) {
	var pin map[string]string
	if secret := a.pinnedSecret(h, home); secret != "" {
		pin = map[string]string{pinnedSecretKey: secret}
	}
	return a.scopedManagerWith(h, home, executable, pin)
}

// scopedManagerWith applies pin last. Each pin value must be an account home
// next to home, for a harness that pins credentials per account.
func (a *Accounts) scopedManagerWith(h, home, executable string, pin map[string]string) (native.Manager, map[string]string, error) {
	for _, v := range pin {
		if !pinsCredentials(h) || !pinnedSecretOf(home, v) {
			return nil, nil, errAccountPath
		}
	}
	roots := harnesshome.Roots(home)
	discovery := harnesshome.Discovery(home)
	if err := checkPrivateAccountHome(h, home, false); err != nil {
		return nil, nil, err
	}
	factory := a.NativeManager
	if factory == nil {
		factory = NativeRuntimeManager
	}
	if h == "opencode" {
		if err := ensureOpenCodeServicePort(home); err != nil {
			return nil, nil, err
		}
	}
	host := native.Host{OpenCodeService: h == "opencode", Home: harnesshome.Home(home), Executable: executable, Env: func(k string) string {
		if v, ok := pin[k]; ok {
			return v
		}
		if v, ok := roots[k]; ok {
			return v
		}
		if v, ok := discovery[k]; ok {
			return v
		}
		return os.Getenv(k)
	}}
	base, ok := factory(h, host)
	if !ok {
		return nil, nil, native.ErrUnsupported
	}
	overrides := harnesshome.Discovery(home)
	// Native login adapters supply the complete auth/provider clearing set.
	_, env, err := base.LoginCommand("", "")
	if err != nil {
		return nil, nil, err
	}
	for _, v := range env {
		k, val, ok := strings.Cut(v, "=")
		// Canonical private roots are merged after provider clearing values.
		if ok && k != "HOME" && !strings.HasPrefix(k, "XDG_") {
			overrides[k] = val
		}
	}
	if adapter, e := Adapter(h); e == nil {
		if adapter.AccountBlocked != "" {
			return nil, nil, errors.New(adapter.AccountBlocked)
		}
		if adapter.NativeEnv != nil {
			for k, v := range adapter.NativeEnv(home) {
				if k == "HOME" || strings.HasPrefix(k, "XDG_") {
					return nil, nil, native.ErrUnsupported
				}
				overrides[k] = v
			}
		}
	}
	switch h {
	case "codex":
		overrides["OPENAI_API_KEY"] = ""
		overrides["CODEX_API_KEY"] = ""
	}
	for k, v := range roots {
		overrides[k] = v
	}
	host.Env = func(k string) string {
		if v, ok := overrides[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	if err := validateAccountManagerEnv(h, home, overrides); err != nil {
		return nil, nil, err
	}
	for k, v := range pin {
		overrides[k] = v
	}
	manager, ok := factory(h, host)
	if !ok {
		return nil, nil, native.ErrUnsupported
	}
	if h == "muse" || h == "opencode" || h == "kimi-code" {
		manager = checkedHomeManager{Manager: manager, harness: h, scope: home}
	}
	return manager, overrides, nil
}

func (a *Accounts) activeScope(id, executable string) (NativeAccount, native.Manager, map[string]string, error) {
	n, err := a.Find(id)
	if err != nil {
		return n, nil, nil, err
	}
	if err := requirePrivateSnapshot(n); err != nil {
		return n, nil, nil, err
	}
	if !n.Active {
		return n, nil, nil, errors.New("Select this account before starting a harness turn.")
	}
	home, err := a.HarnessHome(n.Harness)
	if err != nil {
		return n, nil, nil, err
	}
	m, env, err := a.scopedManager(n.Harness, home, executable)
	return n, m, env, err
}

func (a *Accounts) Env(id string) (map[string]string, error) {
	_, _, env, err := a.activeScope(id, "")
	return env, err
}

// launchScope returns the account a turn resolved and its native home. A turn
// may resolve to an account that another harness's provider selection pushed
// aside, so this scope checks no active mark: the turn's own selection already
// chose the account. A harness that keeps credentials per account reads the
// folder of the account this turn resolved, because a room choice can differ
// from the globally active account.
func (a *Accounts) launchScope(id, executable string) (NativeAccount, native.Manager, map[string]string, error) {
	n, err := a.Find(id)
	if err != nil {
		return n, nil, nil, err
	}
	if err := requirePrivateSnapshot(n); err != nil {
		return n, nil, nil, err
	}
	home, err := a.HarnessHome(n.Harness)
	if err != nil {
		return n, nil, nil, err
	}
	var pin map[string]string
	if pinsCredentials(n.Harness) {
		pin = map[string]string{pinnedSecretKey: n.Home}
	}
	m, env, err := a.scopedManagerWith(n.Harness, home, executable, pin)
	return n, m, env, err
}

// launchEnv returns the launch environment of the account a turn resolved.
func (a *Accounts) launchEnv(id string) (map[string]string, error) {
	_, _, env, err := a.launchScope(id, "")
	return env, err
}

// launchManager returns the manager of the account a turn resolved.
func (a *Accounts) launchManager(id, executable string) (native.Manager, error) {
	n, err := a.Find(id)
	if err != nil {
		return nil, err
	}
	if n.Harness == "pi" {
		// Pi reads its whole agent directory from the account's own home.
		if err := requirePrivateSnapshot(n); err != nil {
			return nil, err
		}
		m, _, err := a.scopedManager(n.Harness, n.Home, executable)
		return m, err
	}
	if pinsCredentials(n.Harness) {
		// Readiness reads the account's own credentials folder. The shared
		// config belongs to the globally active account, which a room choice can
		// differ from, and it would answer with that account's identity.
		if err := requirePrivateSnapshot(n); err != nil {
			return nil, err
		}
		return a.pinManager(n, executable)
	}
	_, m, _, err := a.launchScope(id, executable)
	if err != nil {
		return nil, err
	}
	home, _ := a.HarnessHome(n.Harness)
	return privateAccountManager{Manager: m, home: home, nativeRoot: sharedServiceAccount(n.Harness)}, nil
}
func (a *Accounts) Manager(id, executable string) (native.Manager, error) {
	n, err := a.Find(id)
	if err != nil {
		return nil, err
	}
	// A Pi account reads its whole agent directory from its own home, so its
	// manager works there instead of the shared harness home.
	if n.Harness == "pi" {
		if err := requirePrivateSnapshot(n); err != nil {
			return nil, err
		}
		if !n.Active {
			return nil, errors.New("Select this account before starting a harness turn.")
		}
		m, _, err := a.scopedManager(n.Harness, n.Home, executable)
		return m, err
	}
	_, m, _, err := a.activeScope(id, executable)
	if err != nil {
		return nil, err
	}
	home, _ := a.HarnessHome(n.Harness)
	// A pinned harness reads its credentials folder from the environment, not the shared root.
	return privateAccountManager{Manager: m, home: home, nativeRoot: sharedServiceAccount(n.Harness) || pinsCredentials(n.Harness)}, nil
}

// SavedManager reads inactive login snapshots without activating or refreshing them.
func (a *Accounts) SavedManager(id, executable string) (native.Manager, error) {
	n, err := a.Find(id)
	if err != nil {
		return nil, err
	}
	return savedAccountManager{account: n, accounts: a, executable: executable}, nil
}

type savedAccountManager struct {
	account    NativeAccount
	accounts   *Accounts
	executable string
}

func (m savedAccountManager) Harness() string { return m.account.Harness }
func (m savedAccountManager) Caps() native.Caps {
	return native.Caps{Scope: "The saved native login snapshot; no native activation or refresh."}
}
func (m savedAccountManager) Capture(ctx context.Context, root string) ([]native.Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root != "" && root != m.account.Home {
		return nil, errAccountPath
	}
	if err := safeAccountPath(m.account.Home); err != nil {
		return nil, err
	}
	if err := checkAccountFiles(m.account.Harness, m.account.Home, false); err != nil {
		return nil, err
	}
	if err := requirePrivateSnapshot(m.account); err != nil {
		return nil, err
	}
	c, err := readAccountLogin(m.account.Home)
	if err != nil {
		return nil, err
	}
	if c.Identity != m.account.Identity {
		return nil, native.ErrNoIdentity
	}
	return []native.Captured{c}, nil
}
func (m savedAccountManager) Activate(context.Context, string, store.Credential) ([]native.Captured, error) {
	return nil, native.ErrUnsupported
}
func (m savedAccountManager) Refresh(context.Context, native.Captured) (native.Captured, error) {
	return native.Captured{}, native.ErrUnsupported
}
func (m savedAccountManager) LoginCommand(string, string) ([]string, []string, error) {
	return nil, nil, native.ErrUnsupported
}
func (m savedAccountManager) ProbeSaved(ctx context.Context, c native.Captured) (store.Observation, error) {
	if err := requirePrivateSnapshot(m.account); err != nil {
		return store.Observation{}, err
	}
	if c.Identity != m.account.Identity {
		return store.Observation{}, native.ErrNoIdentity
	}
	home, err := m.accounts.HarnessHome(m.account.Harness)
	if err != nil {
		return store.Observation{}, err
	}
	backend, _, err := m.accounts.scopedManager(m.account.Harness, home, m.executable)
	if err != nil {
		return store.Observation{}, err
	}
	prober, ok := backend.(native.SavedProber)
	if !ok {
		return store.Observation{}, native.ErrUnsupported
	}
	return prober.ProbeSaved(ctx, c)
}

// Login prepares fresh sign-in in a saved slot. Shared-service logins need an
// explicit workflow that holds the harness switch gate through login completion.
func (a *Accounts) Login(id, executable string) (argv, env []string, cwd string, err error) {
	n, err := a.Find(id)
	if err != nil {
		return nil, nil, "", err
	}
	if sharedServiceAccount(n.Harness) {
		return nil, nil, "", errors.New("Shared-service sign-in requires a gated native login workflow.")
	}
	err = a.transaction(func(state *store.State) error {
		n, e := a.native(state.Find(id))
		if e != nil {
			return e
		}
		if e = checkAccountFiles(n.Harness, n.Home, true); e != nil {
			return e
		}
		m, overrides, e := a.scopedManager(n.Harness, n.Home, executable)
		if e != nil {
			return e
		}
		if e = prepareAccountLogin(n.Harness, n.Home, overrides); e != nil {
			return e
		}
		// Host.Env already binds the saved scope; root-empty avoids HOME/XDG remaps.
		argv, _, e = m.LoginCommand("", "")
		if e != nil {
			return e
		}
		if adapter, e := Adapter(n.Harness); e == nil && adapter.LoginArgs != nil {
			argv = adapter.LoginArgs(executable, n.Home)
		}
		if n.Harness == "codex" && len(argv) > 0 {
			argv = append([]string{argv[0], "-c", "check_for_update_on_startup=false", "-c", `cli_auth_credentials_store="file"`}, argv[1:]...)
		}
		for k, v := range overrides {
			env = append(env, k+"="+v)
		}
		sort.Strings(env)
		cwd = n.Home
		return nil
	}, false)
	return
}

// accountLoginFiles lists the login files that Dyna reads or writes in an
// account home: its own login snapshots and the harness's native credential
// files. Everything else in the home belongs to the harness.
func accountLoginFiles(h, home string) []string {
	files := []string{filepath.Join(home, ".login.json"), filepath.Join(home, ".login-pending.json"), filepath.Join(home, ".private-login.json")}
	if a, err := Adapter(h); err == nil {
		for _, name := range a.AuthFiles {
			files = append(files, filepath.Join(home, name))
		}
	}
	return append(files, privateAccountFiles(h, home)...)
}

// checkAccountFiles checks only the login files. Each one must be absent or a
// regular file with one link, so it cannot alias a login elsewhere. Skills,
// caches and links in the rest of the home never block a turn.
func checkAccountFiles(h, home string, setModes bool) error {
	if err := safeAccountPath(home); err != nil {
		return err
	}
	if setModes {
		if err := chmodAccountNode(home, 0700, false); err != nil {
			return err
		}
	}
	for _, path := range accountLoginFiles(h, home) {
		if err := checkPrivateAccountFile(path, setModes); err != nil {
			return err
		}
	}
	return nil
}

// Open the checked inode without following a replacement symlink before chmod.
func chmodAccountNode(path string, mode os.FileMode, mayDisappear bool) error {
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if mode == 0700 {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		if mayDisappear && errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return errAccountPath
	}
	f := os.NewFile(uintptr(fd), "native-account-node")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return errAccountPath
	}
	if mode == 0700 {
		if !info.IsDir() {
			return errAccountPath
		}
	} else {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode().IsRegular() && ok && stat.Nlink == 0 && mayDisappear {
			return nil
		}
		if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
			return errAccountPath
		}
	}
	return f.Chmod(mode)
}

// Capture records a completed saved-slot sign-in without refreshing or calling a provider.
func (a *Accounts) Capture(ctx context.Context, id, executable string) (result NativeAccount, err error) {
	err = a.transaction(func(state *store.State) error {
		entry := state.Find(id)
		n, e := a.native(entry)
		if e != nil {
			return e
		}
		if sharedServiceAccount(n.Harness) {
			return native.ErrUnsupported
		}
		if e = checkAccountFiles(n.Harness, n.Home, true); e != nil {
			return e
		}
		m, _, e := a.scopedManager(n.Harness, n.Home, executable)
		if e != nil {
			return e
		}
		captured, e := m.Capture(ctx, "")
		if errors.Is(e, native.ErrNotSignedIn) {
			entry.NeedsLogin = true
		} else {
			if e != nil {
				return e
			}
			if len(captured) != 1 || captured[0].Identity == "" {
				return native.ErrNoIdentity
			}
			if e = writePrivateAccountJSON(filepath.Join(n.Home, ".login-pending.json"), captured[0]); e != nil {
				return e
			}
			entry.Identity = captured[0].Identity
			entry.Integration = captured[0].Integration
			if entry.Label == "" {
				entry.Label = captured[0].Label
			}
			entry.NeedsLogin = false
		}
		entry.UpdatedAt = time.Now().UTC()
		result, e = a.native(entry)
		return e
	}, true)
	return
}

// Saved login snapshots are private credential files, never index metadata.
func writeAccountLogin(home string, c native.Captured) error {
	if c.Identity == "" || c.Credential.Schema == "" || !json.Valid(c.Credential.Payload) {
		return native.ErrSchema
	}
	if err := safeAccountPath(filepath.Join(home, ".login.json")); err != nil {
		return err
	}
	return writePrivateAccountJSON(filepath.Join(home, ".login.json"), c)
}
func readAccountLogin(home string) (native.Captured, error) {
	pending := filepath.Join(home, ".login-pending.json")
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		return readAccountLoginFile(pending)
	}
	return readAccountLoginFile(filepath.Join(home, ".login.json"))
}
func readAccountLoginFile(path string) (native.Captured, error) {
	var c native.Captured
	if err := safeAccountPath(path); err != nil {
		return c, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return c, native.ErrNotSignedIn
	}
	if err != nil {
		return c, errAccountPath
	}
	f := os.NewFile(uintptr(fd), "saved-login")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return c, errAccountPath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return c, errAccountPath
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if err != nil || len(raw) > 16<<20 || json.Unmarshal(raw, &c) != nil || c.Identity == "" {
		return c, native.ErrSchema
	}
	return c, nil
}

// IsAccountPath also protects aliases through symlinks and paths not yet created.
func IsAccountPath(path string) bool {
	root, err := filepath.Abs(NewAccounts().Root)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	within := func(base, p string) bool {
		rel, e := filepath.Rel(base, p)
		return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if within(root, path) {
		return true
	}
	resolve := func(p string) string {
		tail := []string{}
		for {
			resolved, e := filepath.EvalSymlinks(p)
			if e == nil {
				for i := len(tail) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, tail[i])
				}
				return resolved
			}
			parent := filepath.Dir(p)
			if parent == p {
				return p
			}
			tail = append(tail, filepath.Base(p))
			p = parent
		}
	}
	return within(resolve(root), resolve(path))
}

// ProbeSaved reads usage for a captured native grant without saving or rotating it.
func (m privateAccountManager) ProbeSaved(ctx context.Context, c native.Captured) (store.Observation, error) {
	p, ok := m.Manager.(native.SavedProber)
	if !ok {
		return store.Observation{}, native.ErrUnsupported
	}
	return p.ProbeSaved(ctx, c)
}
