package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	harnesshome "dyna-agent/internal/harness/home"
)

func ownerImportFixture(t *testing.T) (*Runtime, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearRuntimeSelectors(t)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "CURSOR_API_ENDPOINT", "CURSOR_API_BASE_URL", "META_API_KEY", "TBH_CREDENTIAL_BACKEND", "OPENCODE_DISABLE_CHANNEL_DB", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	return &Runtime{Accounts: &Accounts{Root: filepath.Join(t.TempDir(), "accounts")}}, home
}
func ownerImportWrite(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}
func ownerImportSeed(t *testing.T, home, h string) []string {
	t.Helper()
	var files map[string]string
	switch h {
	case "claude-code":
		files = map[string]string{".claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":4102444800000,"scopes":["user:inference"]},"mcpOAuth":{"canary":"owner-only"}}`, ".claude.json": `{"oauthAccount":{"accountUuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},"projects":{"canary":"owner-only"},"mcpServers":{"canary":"owner-only"}}`}
	case "pi":
		files = map[string]string{".pi/agent/auth.json": `{"anthropic":{"type":"oauth","access":"fixture-access","refresh":"fixture-refresh"}}`}
	case "grok":
		files = map[string]string{".grok/auth.json": `{"access_token":"fixture-access","refresh_token":"fixture-refresh"}`}
	}
	files["settings-canary.json"] = `{"skills":"owner-only","hooks":"owner-only","plugins":"owner-only","mcpServers":"owner-only"}`
	var paths []string
	for name, raw := range files {
		path := filepath.Join(home, name)
		ownerImportWrite(t, path, raw)
		paths = append(paths, path)
	}
	return paths
}
func TestOwnerLoginImportNativeFiles(t *testing.T) {
	for _, h := range []string{"claude-code", "pi", "grok"} {
		t.Run(h, func(t *testing.T) {
			r, home := ownerImportFixture(t)
			paths := ownerImportSeed(t, home, h)
			before := map[string]string{}
			for _, p := range paths {
				b, _ := os.ReadFile(p)
				before[p] = string(b)
			}
			// Existing provider key clearing must still allow the native file login.
			t.Setenv("OPENAI_API_KEY", "inherited-key")
			t.Setenv("ANTHROPIC_API_KEY", "inherited-key")
			t.Setenv("META_API_KEY", "inherited-key")
			if err := r.copyPersonalLogin(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			n, err := r.Accounts.Active(accountProvider(h), h)
			if err != nil || n.Status != "ready" {
				t.Fatalf("private account unavailable: %v, %s", err, n.Status)
			}
			scope, _ := r.Accounts.HarnessHome(h)
			if pinsCredentials(h) {
				// A turn start moves the copied login into the account's own home.
				if err = r.Accounts.prepareTurnPin(context.Background(), n, ""); err != nil {
					t.Fatal(err)
				}
			}
			m, env, err := r.Accounts.scopedManager(h, scope, "")
			if err != nil {
				t.Fatal(err)
			}
			actual, err := m.Capture(context.Background(), "")
			if err != nil || len(actual) != 1 || actual[0].Identity != n.Identity {
				t.Fatal("real private capture failed", err)
			}
			saved, err := readAccountLogin(n.Home)
			if err != nil || !sameNativeLogin(actual[0], saved) {
				t.Fatal("snapshot differs from private login")
			}
			if env["HOME"] != harnesshome.Home(scope) {
				t.Fatal("private HOME missing")
			}
			for _, p := range paths {
				b, _ := os.ReadFile(p)
				if string(b) != before[p] {
					t.Fatal("owner file changed")
				}
			}
			for _, root := range []string{scope, harnesshome.Home(scope), n.Home} {
				err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if d.IsDir() {
						return nil
					}
					b, e := os.ReadFile(path)
					if e != nil {
						return e
					}
					if strings.Contains(string(b), "owner-only") {
						t.Error("owner configuration entered private storage")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Logout must not cause a second copy, even when the owner still has grants.
			var login string
			switch h {
			case "claude-code":
				login = filepath.Join(scope, ".credentials.json")
			default:
				login = filepath.Join(scope, "auth.json")
			}
			if err := os.Remove(login); err != nil {
				t.Fatal(err)
			}
			if _, err := r.UsePersonalLogin(context.Background(), h); err == nil {
				t.Fatal("a saved login was copied again")
			}
			if _, err := os.Lstat(login); !os.IsNotExist(err) {
				t.Fatal("logout was imported again")
			}
		})
	}
}
func TestCopyPersonalLoginMissingOrBroken(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "broken"}[broken], func(t *testing.T) {
			r, home := ownerImportFixture(t)
			if broken {
				ownerImportWrite(t, filepath.Join(home, ".grok/auth.json"), "invalid-json")
			}
			if err := r.copyPersonalLogin(context.Background(), "grok"); !errors.Is(err, errNoPersonalLogin) {
				t.Fatal("missing login was not reported", err)
			}
			if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); !errors.Is(err, errAccountMissing) {
				t.Fatal("failed copy did not leave Sign in")
			}
			// The failed attempt leaves nothing behind, so a later choice copies.
			ownerImportSeed(t, home, "grok")
			if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
				t.Fatal(err)
			}
			if n, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil || n.Status != "ready" {
				t.Fatal("later copy failed", err)
			}
		})
	}
}
func TestCopyPersonalLoginNeverReplacesSavedLogin(t *testing.T) {
	for _, h := range []string{"claude-code", "grok"} {
		t.Run(h, func(t *testing.T) {
			r, home := ownerImportFixture(t)
			ownerImportSeed(t, home, h)
			if err := r.copyPersonalLogin(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			n, _ := r.Accounts.Active(accountProvider(h), h)
			saved, _ := readAccountLogin(n.Home)
			for _, path := range ownerImportSeed(t, home, h) {
				raw, _ := os.ReadFile(path)
				ownerImportWrite(t, path, strings.ReplaceAll(string(raw), "fixture-access", "changed-owner-access"))
			}
			if err := r.copyPersonalLogin(context.Background(), h); !errors.Is(err, errPrivateHomeUsed) {
				t.Fatal("second copy was not refused", err)
			}
			current, err := r.Accounts.Active(accountProvider(h), h)
			if err != nil || current.ID != n.ID || current.Status != "ready" {
				t.Fatal("existing account changed", err)
			}
			actual, _ := readAccountLogin(n.Home)
			if string(actual.Credential.Payload) != string(saved.Credential.Payload) {
				t.Fatal("saved login overwritten")
			}
		})
	}
}
func TestOwnerLoginImportIgnoresNestedAliases(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	nested := filepath.Join(r.Accounts.Root, "grok", "parent-runtime")
	ownerImportWrite(t, filepath.Join(nested, "auth.json"), `{"access_token":"parent-private"}`)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_HOME", alias)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(alias, "not-created"))
	host, err := ownerLoginHost(r.Accounts)
	if err != nil || host.Getenv("GROK_HOME") != filepath.Join(home, ".grok") || host.Getenv("XDG_CONFIG_HOME") != "" {
		t.Fatal("private path alias was not ignored")
	}
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	n, err := r.Accounts.Active(accountProvider("grok"), "grok")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := readAccountLogin(n.Home)
	if strings.Contains(string(c.Credential.Payload), "parent-private") {
		t.Fatal("parent private login imported")
	}
}
func TestOwnerLoginImportSerializesWithTurnLease(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "pi")
	// The copies serialize on the import lock, one after another, and each
	// bounds itself. A shared wall-clock deadline only raced machine load;
	// the test's own context still stops a hang.
	ctx := t.Context()
	release, err := r.Accounts.TurnLease(ctx, "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var wg sync.WaitGroup
	var copied atomic.Int32
	for range 4 {
		wg.Go(func() {
			if err := r.copyPersonalLogin(ctx, "pi"); err == nil {
				copied.Add(1)
			} else if !errors.Is(err, errPrivateHomeUsed) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := r.Accounts.List()
	if err != nil || len(entries) != 1 || copied.Load() != 1 {
		t.Fatal("concurrent copies were not serialized", err, copied.Load())
	}
}

type ownerImportPartialManager struct {
	native.Manager
	calls *int
}

func (m ownerImportPartialManager) Activate(ctx context.Context, integration string, c store.Credential) ([]native.Captured, error) {
	*m.calls++
	rows, err := m.Manager.Activate(ctx, integration, c)
	if err != nil {
		return rows, err
	}
	return rows, native.ErrPartialActivation
}

func TestOwnerLoginImportPartialFailureNeverRecopies(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	calls := 0
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		if strings.HasPrefix(host.Home, r.Accounts.Root+string(filepath.Separator)) {
			return ownerImportPartialManager{m, &calls}, ok
		}
		return m, ok
	}
	p := "grok"
	if err := r.copyPersonalLogin(context.Background(), p); !errors.Is(err, native.ErrPartialActivation) {
		t.Fatal("partial activation was not reported", err)
	}
	scope, _ := r.Accounts.HarnessHome("grok")
	path := filepath.Join(scope, "auth.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ownerImportWrite(t, filepath.Join(home, ".grok/auth.json"), `{"access_token":"replacement-owner-grant"}`)
	if err := r.copyPersonalLogin(context.Background(), p); !errors.Is(err, errPrivateHomeUsed) {
		t.Fatal("partial copy was not protected", err)
	}
	after, _ := os.ReadFile(path)
	if calls != 1 || string(before) != string(after) {
		t.Fatal("partial failure was copied again")
	}
	if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); !errors.Is(err, errAccountMissing) {
		t.Fatal("partial failure was marked active")
	}
}
func TestOwnerLoginImportPreservesUnindexedNativeLogin(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	scope, _ := r.Accounts.HarnessHome("grok")
	path := filepath.Join(scope, "auth.json")
	ownerImportWrite(t, path, `{"access_token":"existing-private"}`)
	if err := r.copyPersonalLogin(context.Background(), "grok"); !errors.Is(err, errPrivateHomeUsed) {
		t.Fatal("copy into a used private home was not refused", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "existing-private") {
		t.Fatal("unindexed private login overwritten")
	}
}

func TestOwnerLoginImportEmptySavedSlot(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	if _, err := r.Accounts.Create("grok", "empty"); err != nil {
		t.Fatal(err)
	}
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil || n.Status != "ready" {
		t.Fatal("empty saved slot prevented import", err)
	}
}

type ownerImportCancelAfterCapture struct {
	native.Manager
	cancel context.CancelFunc
}

func (m ownerImportCancelAfterCapture) Capture(ctx context.Context, root string) ([]native.Captured, error) {
	rows, err := m.Manager.Capture(ctx, root)
	if err == nil {
		m.cancel()
	}
	return rows, err
}

// Cancellation after activation must not turn a completed import into a failure.
func TestOwnerLoginImportSuccessAfterCancellation(t *testing.T) {
	for _, h := range []string{"grok", "pi"} {
		t.Run(h, func(t *testing.T) {
			r, home := ownerImportFixture(t)
			ownerImportSeed(t, home, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
				m, ok := NativeRuntimeManager(h, host)
				if host.Home != home {
					// Cancel after the private capture verifies the activated login.
					return ownerImportCancelAfterCapture{m, cancel}, ok
				}
				return m, ok
			}
			if err := r.copyPersonalLogin(ctx, h); err != nil {
				t.Fatal("completed import reported cancellation", err)
			}
			if ctx.Err() != context.Canceled {
				t.Fatal("private capture did not cancel the context")
			}
			if n, err := r.Accounts.Active(accountProvider(h), h); err != nil || n.Status != "ready" {
				t.Fatal("completed import did not activate the private account", err)
			}
			pending := filepath.Join(r.Accounts.Root, h, ".owner-login-import.pending")
			if _, err := os.Lstat(pending); !os.IsNotExist(err) {
				t.Fatal("completed import left its pending file", err)
			}
		})
	}
}

func TestOwnerLoginImportBrokenPrivateLoginDoesNotRecopy(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	p := "grok"
	if err := r.copyPersonalLogin(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	scope, _ := r.Accounts.HarnessHome("grok")
	path := filepath.Join(scope, "auth.json")
	ownerImportWrite(t, path, "failed-private-login")
	if err := r.copyPersonalLogin(context.Background(), p); !errors.Is(err, errPrivateHomeUsed) {
		t.Fatal("copy over a broken private login was not refused", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "failed-private-login" {
		t.Fatal("broken private login was replaced")
	}
	m, _, err := r.Accounts.scopedManager("grok", scope, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Capture(context.Background(), ""); !errors.Is(err, native.ErrSchema) {
		t.Fatal("broken login did not retain Sign in state")
	}
}

type ownerImportReadOnlyManager struct {
	native.Manager
	owner                  bool
	refreshes, ownerWrites *int
}

func (m ownerImportReadOnlyManager) Caps() native.Caps {
	c := m.Manager.Caps()
	c.Refresh = true
	return c
}
func (m ownerImportReadOnlyManager) Activate(ctx context.Context, integration string, c store.Credential) ([]native.Captured, error) {
	if m.owner {
		(*m.ownerWrites)++
		return nil, errors.New("owner writes are forbidden")
	}
	return m.Manager.Activate(ctx, integration, c)
}
func (m ownerImportReadOnlyManager) Refresh(context.Context, native.Captured) (native.Captured, error) {
	(*m.refreshes)++
	if m.owner {
		(*m.ownerWrites)++
	}
	return native.Captured{}, native.ErrInvalidGrant
}
func (m ownerImportReadOnlyManager) CredentialExpiry(native.Captured) (time.Time, error) {
	return time.Unix(1, 0), nil
}
func TestOwnerLoginImportOwnerCanariesAfterFailedRefresh(t *testing.T) {
	for _, h := range []string{"claude-code", "pi", "grok"} {
		t.Run(h, func(t *testing.T) {
			r, home := ownerImportFixture(t)
			files := ownerImportSeed(t, home, h)
			bytesBefore := map[string]string{}
			timesBefore := map[string]time.Time{}
			for _, path := range files {
				fixed := time.Unix(1000000000, 0)
				if err := os.Chtimes(path, fixed, fixed); err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(path)
				bytesBefore[path] = string(raw)
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				timesBefore[path] = st.ModTime()
			}
			checkOwner := func() {
				t.Helper()
				for _, path := range files {
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					st, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if string(raw) != bytesBefore[path] || !st.ModTime().Equal(timesBefore[path]) {
						t.Fatal("owner bytes or mtime changed")
					}
				}
			}
			refreshes, ownerWrites := 0, 0
			r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
				m, ok := NativeRuntimeManager(h, host)
				return ownerImportReadOnlyManager{m, host.Home == home, &refreshes, &ownerWrites}, ok
			}
			p := h
			if err := r.copyPersonalLogin(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if refreshes != 0 || ownerWrites != 0 {
				t.Fatal("import refreshed or wrote owner credentials")
			}
			checkOwner()
			n, err := r.Accounts.Active(accountProvider(h), h)
			if err != nil {
				t.Fatal(err)
			}
			// Refresh the private saved snapshot through the existing public flow. The
			// offline backend returns invalid_grant; no token reaches a provider.
			saved, err := readAccountLogin(n.Home)
			if err != nil {
				t.Fatal(err)
			}
			saved.ExpiresAt = time.Unix(1, 0)
			if err := r.Accounts.transaction(func(s *store.State) error { s.Find(n.ID).NeedsLogin = true; return nil }, true); err != nil {
				t.Fatal(err)
			}
			if err := writeAccountLogin(n.Home, saved); err != nil {
				t.Fatal(err)
			}
			if err := r.Accounts.setActive(h, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Accounts.RefreshCredentials(context.Background(), n.ID, ""); !errors.Is(err, native.ErrInvalidGrant) {
				t.Fatal("private refresh did not fail as expected", err)
			}
			if refreshes != 1 || ownerWrites != 0 {
				t.Fatal("failed private refresh reached owner storage")
			}
			checkOwner()
			if err := r.copyPersonalLogin(context.Background(), p); !errors.Is(err, errPrivateHomeUsed) {
				t.Fatal("second copy was not refused", err)
			}
			checkOwner()
			found, err := r.Accounts.Find(n.ID)
			if err != nil || found.Status != "not signed in" {
				t.Fatal("failed private refresh did not retain Sign in", err)
			}
		})
	}
}

func TestOwnerLoginImportBusyLockLeavesAttemptAvailable(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	dir, err := r.Accounts.harnessDirectory("grok")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireAccountLock(context.Background(), filepath.Join(dir, ".owner-login-import.lock"), syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = r.copyPersonalLogin(ctx, "grok")
	releaseAccountLock(lock)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errOwnerLoginRetry) {
		t.Fatal("busy import did not return a retryable error", err)
	}
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil {
		t.Fatal("retry could not import", err)
	}
}

type ownerImportObservedCapture struct {
	native.Manager
	before func()
}

func (m ownerImportObservedCapture) Capture(ctx context.Context, root string) ([]native.Captured, error) {
	m.before()
	return m.Manager.Capture(ctx, root)
}

type ownerImportStalledCapture struct{ native.Manager }

func (ownerImportStalledCapture) Capture(ctx context.Context, _ string) ([]native.Captured, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestOwnerLoginImportDeadlineLeavesAttemptAvailable(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		if host.Home == home {
			return ownerImportStalledCapture{m}, ok
		}
		return m, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := r.copyPersonalLogin(ctx, "grok")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errOwnerLoginRetry) {
		t.Fatal("deadline did not return a retryable error", err)
	}
	r.Accounts.NativeManager = nil
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil {
		t.Fatal("retry could not import", err)
	}
}

func TestOwnerLoginImportRetryIndexesInterruptedCopy(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	dir := filepath.Join(r.Accounts.Root, "grok")
	// A pending file with an empty private scope still copies the owner login.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ownerImportWrite(t, filepath.Join(dir, ".owner-login-import.pending"), `{"version":1}`)
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil {
		t.Fatal("pending file with an empty scope lost the import", err)
	}
	r, home = ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	dir = filepath.Join(r.Accounts.Root, "grok")
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		if host.Home != home {
			return ownerImportStalledCapture{m}, ok
		}
		return m, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.copyPersonalLogin(ctx, "grok"); !errors.Is(err, errOwnerLoginRetry) {
		t.Fatal("interrupted index did not return a retryable error", err)
	}
	// The retry must index the private copy without reading the owner again.
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		if host.Home == home {
			return ownerImportObservedCapture{m, func() { t.Error("retry read the owner login again") }}, ok
		}
		return m, ok
	}
	if err := r.copyPersonalLogin(context.Background(), "grok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil {
		t.Fatal("retry did not index the private copy", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".owner-login-import.pending")); !os.IsNotExist(err) {
		t.Fatal("finished attempt left its pending file")
	}
}

// A legacy grant-only entry with the same identity must not become active.
func TestOwnerLoginImportActivatesPrivateEntryOverLegacyGrant(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "claude-code")
	err := r.Accounts.transaction(func(s *store.State) error {
		s.Entries = append(s.Entries, store.Entry{Metadata: store.Metadata{ID: "legacy", Harness: "claude-code", Identity: "anthropic:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}})
		return nil
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.copyPersonalLogin(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	n, err := r.Accounts.Active(accountProvider("claude-code"), "claude-code")
	if err != nil || n.ID == "legacy" || n.Home == "" {
		t.Fatalf("active account = %+v, %v; want the private entry", n, err)
	}
}

type ownerImportFailingManager struct{ native.Manager }

func (ownerImportFailingManager) Capture(context.Context, string) ([]native.Captured, error) {
	return nil, errors.New("Grok account command failed.")
}

// A failed native command is transient: the copy reports it, and the next
// attempt copies the login.
func TestOwnerLoginImportRetriesAfterFailedCommand(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "grok")
	p := "grok"
	r.Accounts.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		return ownerImportFailingManager{m}, ok
	}
	err := r.copyPersonalLogin(context.Background(), p)
	if !errors.Is(err, errOwnerLoginRetry) || !strings.Contains(err.Error(), "Grok account command failed.") {
		t.Fatalf("failed command was not reported for retry: %v", err)
	}
	r.Accounts.NativeManager = nil
	if err := r.copyPersonalLogin(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Accounts.Active(accountProvider("grok"), "grok"); err != nil || n.Status != "ready" {
		t.Fatal("retry did not import the login", err)
	}
}
