package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountnative "dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/store"
)

func signedCodex(t *testing.T, a *Accounts, user, token string) NativeAccount {
	t.Helper()
	n, err := a.Create("codex", user)
	if err != nil {
		t.Fatal(err)
	}
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"`+user+`","chatgpt_account_id":"workspace"}}`)) + ".signature"
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": jwt, "access_token": "fake-access", "refresh_token": token, "account_id": "workspace"}})
	if err = os.WriteFile(filepath.Join(n.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err = a.Capture(context.Background(), n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func awaitAccountCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !fn() {
		select {
		case <-deadline.C:
			t.Fatal("account condition timed out")
		case <-ticker.C:
		}
	}
}

// This helper is a separate process. Its kernel lease survives no process exit.
func TestAccountLeaseProcess(t *testing.T) {
	if os.Getenv("TEST_NATIVE_ACCOUNT_HELPER") != "1" {
		return
	}
	a := &Accounts{Root: os.Getenv("TEST_NATIVE_ACCOUNT_ROOT")}
	release, err := a.TurnLease(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TEST_NATIVE_ACCOUNT_CRASH") == "1" {
		os.Exit(0)
	}
	control := make([]byte, 1)
	if _, err = io.ReadFull(os.Stdin, control); err != nil {
		t.Fatal(err)
	}
	home, err := a.HarnessHome("codex")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["tokens"].(map[string]any)["refresh_token"] = "rotated-native-canary"
	raw, _ = json.Marshal(doc)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	release()
}

func TestTurnLeaseModesAndSwitchCancellation(t *testing.T) {
	for _, h := range []string{"claude-code", "codex", "opencode", "antigravity", "muse", "grok", "pi"} {
		t.Run(h, func(t *testing.T) {
			a := testAccounts(t)
			release, err := a.TurnLease(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			adapter, _ := Adapter(h)
			// A shared turn must start without waiting; its generous
			// deadline only bounds a regression, so a slow runner passes.
			// A serialized turn must wait, and its short deadline always
			// expires.
			wait := 30 * time.Second
			if adapter.Exclusive {
				wait = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			another, err := (&Accounts{Root: a.Root}).TurnLease(ctx, h)
			if !adapter.Exclusive {
				if err != nil {
					t.Fatal(err)
				}
				defer another()
				_, running := a.SwitchStatus(h)
				if running != 2 {
					t.Fatal("shared turns were serialized", running)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				if another != nil {
					another()
				}
				t.Fatal("unverified harness ran concurrently", err)
			}
		})
	}
	a := testAccounts(t)
	n := signedCodex(t, a, "target", "fake-native")
	release, err := a.TurnLease(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Switch(ctx, n.ID, "/fake/codex") }()
	awaitAccountCondition(t, func() bool { pending, _ := a.SwitchStatus("codex"); return pending })
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	pending, running := a.SwitchStatus("codex")
	if pending || running != 1 {
		t.Fatal(pending, running)
	}
	more, err := a.TurnLease(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	more()
	if err = a.Remove(n.ID, true); err == nil {
		t.Fatal("account removed during a live turn")
	}
}

type partialAccountManager struct {
	accountnative.Manager
	fail *atomic.Bool
}

func (m partialAccountManager) Activate(ctx context.Context, integration string, c accountstore.Credential) ([]accountnative.Captured, error) {
	previous, err := m.Manager.Activate(ctx, integration, c)
	if err == nil && m.fail.CompareAndSwap(true, false) {
		return previous, accountnative.ErrPartialActivation
	}
	return previous, err
}
func TestSwitchRestoresPreviousLoginAfterPartialActivation(t *testing.T) {
	a := testAccounts(t)
	first := signedCodex(t, a, "first", "outgoing-login")
	second := signedCodex(t, a, "second", "target-login")
	if err := a.Switch(context.Background(), first.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	home, _ := a.HarnessHome("codex")
	fail := &atomic.Bool{}
	fail.Store(true)
	a.NativeManager = func(h string, host accountnative.Host) (accountnative.Manager, bool) {
		m, ok := accountnative.For(h, host)
		if ok && host.Getenv("CODEX_HOME") == home {
			return partialAccountManager{m, fail}, true
		}
		return m, ok
	}
	if err := a.Switch(context.Background(), second.ID, "/fake/codex"); !errors.Is(err, accountnative.ErrPartialActivation) {
		t.Fatal(err)
	}
	active, err := a.Active("codex")
	if err != nil || active.ID != first.ID {
		t.Fatal(active, err)
	}
	m, err := a.Manager(first.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := m.Capture(context.Background(), "")
	if err != nil || len(actual) != 1 || actual[0].Identity != first.Identity {
		t.Fatal("native rollback failed", err)
	}
}

func TestRuntimeSkillsLinkDoesNotExposeCredentialReads(t *testing.T) {
	a := testAccounts(t)
	n := signedCodex(t, a, "first", "fixture-native")
	if err := a.Switch(context.Background(), n.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	home, _ := a.HarnessHome("codex")
	skills := filepath.Join(store.DataDir(), "harnesses", "generations", "fixture", "skills")
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(skills, "SKILL.md")
	if err := os.WriteFile(source, []byte("fixture skills"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skills, filepath.Join(home, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := checkAccountFiles("codex", home, true); err != nil {
		t.Fatal("generated skills link rejected", err)
	}
	checkMode(t, source, 0644)
	m, err := a.Manager(n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Capture(context.Background(), ""); err != nil {
		t.Fatal("skills link blocked native capture", err)
	}
}

type sharedLoginFixture struct {
	mu      sync.Mutex
	current []accountnative.Captured
}
type sharedLoginManager struct {
	h     string
	state *sharedLoginFixture
}

func (m sharedLoginManager) Harness() string          { return m.h }
func (m sharedLoginManager) Caps() accountnative.Caps { return accountnative.Caps{Activate: true} }
func (m sharedLoginManager) Capture(ctx context.Context, root string) ([]accountnative.Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root != "" {
		return nil, accountnative.ErrUnsupported
	}
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if len(m.state.current) == 0 {
		return nil, accountnative.ErrNotSignedIn
	}
	return append([]accountnative.Captured{}, m.state.current...), nil
}

// ActivateNewer lets the fake stand in for a pinned Claude login.
func (m sharedLoginManager) ActivateNewer(ctx context.Context, c accountnative.Captured) (bool, error) {
	_, err := m.Activate(ctx, c.Integration, c.Credential)
	return err == nil, err
}

func (m sharedLoginManager) Activate(ctx context.Context, integration string, c accountstore.Credential) ([]accountnative.Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var payload struct{ Identity string }
	if json.Unmarshal(c.Payload, &payload) != nil || payload.Identity == "" {
		return nil, accountnative.ErrSchema
	}
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	previous := append([]accountnative.Captured{}, m.state.current...)
	m.state.current = []accountnative.Captured{{Identity: payload.Identity, Integration: integration, Credential: c}}
	return previous, nil
}
func (m sharedLoginManager) Refresh(context.Context, accountnative.Captured) (accountnative.Captured, error) {
	return accountnative.Captured{}, errors.New("test forbids saved refresh")
}
func (m sharedLoginManager) LoginCommand(root, integration string) ([]string, []string, error) {
	if root != "" && m.h != "muse" {
		return nil, nil, accountnative.ErrUnsupported
	}
	return []string{"/fake/native", "login"}, nil, nil
}
func fixtureLogin(identity string) accountnative.Captured {
	raw, _ := json.Marshal(map[string]string{"Identity": identity, "nativeLogin": "fake-shared-canary"})
	return accountnative.Captured{Identity: identity, Credential: accountstore.Credential{Schema: "fixture/native@1", Payload: raw}}
}
func setupSharedLogin(t *testing.T, h string) (*Accounts, *sharedLoginFixture, NativeAccount) {
	t.Helper()
	a := testAccounts(t)
	// Native-login state tests use explicit toolchain roots. Host Go discovery
	// is covered by harness/home tests and adds an external process per login.
	tools := t.TempDir()
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		t.Setenv(key, filepath.Join(tools, key))
	}
	state := &sharedLoginFixture{}
	a.NativeManager = func(h string, host accountnative.Host) (accountnative.Manager, bool) {
		if !strings.HasSuffix(host.Home, ".home") || host.Getenv("HOME") != host.Home {
			t.Error("manager lacks private HOME")
		}
		return sharedLoginManager{h, state}, true
	}
	n, err := a.Create(h, "saved")
	if err != nil {
		t.Fatal(err)
	}
	return a, state, n
}
func setSharedLogin(state *sharedLoginFixture, c accountnative.Captured) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.current = []accountnative.Captured{c}
}

func TestSharedServiceNativeLoginCompleteAndCancel(t *testing.T) {
	for _, h := range []string{"opencode", "antigravity", "muse"} {
		t.Run(h, func(t *testing.T) {
			a, state, first := setupSharedLogin(t, h)
			flow, err := a.BeginLogin(context.Background(), first.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			if h == "antigravity" && flow.Warning != "" {
				t.Fatal("stale global keyring warning")
			}
			if h == "muse" && flow.Warning != "" {
				t.Fatal(flow.Warning)
			}

			shared, _ := a.HarnessHome(h)
			if flow.Cwd != shared {
				t.Fatal("login did not use shared state", flow.Cwd)
			}
			for _, env := range flow.Env {
				if strings.HasPrefix(env, "HOME=") && env != "HOME="+shared+".home" {
					t.Fatal(env)
				}
			}
			pending, running := a.SwitchStatus(h)
			if !pending || running != 0 {
				t.Fatal("interactive login released gate", pending, running)
			}
			setSharedLogin(state, fixtureLogin("fixture:first"))
			signed, err := flow.Complete(context.Background())
			if err != nil || !signed.Active || signed.Identity != "fixture:first" {
				t.Fatal(signed, err)
			}
			if _, err = flow.Complete(context.Background()); err != nil {
				t.Fatal("complete is not idempotent", err)
			}
			pending, _ = a.SwitchStatus(h)
			if pending {
				t.Fatal("login gate leaked")
			}
			second, err := a.Create(h, "second")
			if err != nil {
				t.Fatal(err)
			}
			flow, err = a.BeginLogin(context.Background(), second.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			setSharedLogin(state, fixtureLogin("fixture:second"))
			if err = flow.Cancel(); err != nil {
				t.Fatal(err)
			}
			if err = flow.Cancel(); err != nil {
				t.Fatal("cancel is not idempotent", err)
			}
			active, err := a.Active(h)
			if err != nil || active.ID != first.ID {
				t.Fatal(active, err)
			}
			state.mu.Lock()
			identity := state.current[0].Identity
			state.mu.Unlock()
			if identity != "fixture:first" {
				t.Fatal("cancel failed to restore outgoing login")
			}
			// A second successful shared login keeps the same runtime state directory.
			flow, err = a.BeginLogin(context.Background(), second.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			setSharedLogin(state, fixtureLogin("fixture:second"))
			signed, err = flow.Complete(context.Background())
			if err != nil || !signed.Active || signed.ID != second.ID {
				t.Fatal(signed, err)
			}
			if err = a.Switch(context.Background(), first.ID, "/fake/native"); err != nil {
				t.Fatal(err)
			}
			manager, err := a.Manager(first.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			captured, err := manager.Capture(context.Background(), "")
			if err != nil || captured[0].Identity != "fixture:first" {
				t.Fatal("scope-bound saved ref was not activated", err)
			}
			raw := accountMetadataBytes(t, a)
			if strings.Contains(string(raw), "fake-shared-canary") {
				t.Fatal("login entered metadata", err)
			}
		})
	}
}

func TestSavedSlotLoginDoesNotDrainLiveTurns(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	release, err := a.TurnLease(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	writeAccountFixture(t, n)
	signed, err := flow.Complete(context.Background())
	if err != nil || signed.Active {
		t.Fatal("saved-slot login implicitly switched", signed, err)
	}
	pending, running := a.SwitchStatus("codex")
	if pending || running != 1 {
		t.Fatal(pending, running)
	}
}

func useGenericGrokAdapter(t *testing.T) {
	t.Helper()
	adapters.Lock()
	previous, hadPrevious := adapters.items["grok"]
	adapters.items["grok"] = RuntimeAdapter{Harness: "grok", Binary: "grok", AuthFiles: []string{"auth.json"},
		NativeEnv: func(home string) map[string]string { return map[string]string{"GROK_HOME": home} },
		LoginArgs: func(exe, home string) []string { return []string{exe, "login"} },
	}
	adapters.Unlock()
	t.Cleanup(func() {
		adapters.Lock()
		defer adapters.Unlock()
		if hadPrevious {
			adapters.items["grok"] = previous
		} else {
			delete(adapters.items, "grok")
		}
	})
}

func signedGenericGrok(t *testing.T, a *Accounts, label, canary string) NativeAccount {
	t.Helper()
	n, err := a.Create("grok", label)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"access_token": canary})
	if err = os.WriteFile(filepath.Join(n.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err = a.Capture(context.Background(), n.ID, "/fake/grok")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A saved login whose identity no longer matches the native capture must fail
// without saving the unsaved outgoing native login as new accounts.
func TestFailedSwitchSavesNoNewAccounts(t *testing.T) {
	a := testAccounts(t)
	useGenericGrokAdapter(t)
	stale := signedGenericGrok(t, a, "stale", "stale-canary")
	if err := a.transaction(func(state *accountstore.State) error {
		login, err := readAccountLogin(stale.Home)
		if err != nil {
			return err
		}
		login.Identity = "grok:recorded-by-older-dyna"
		state.Find(stale.ID).Identity = login.Identity
		return errors.Join(writeAccountLogin(stale.Home, login), writePrivateAccountOrigin(stale.Home, login.Identity))
	}, true); err != nil {
		t.Fatal(err)
	}
	home, _ := a.HarnessHome("grok")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"access_token":"unsaved-canary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	homes := accountHomes(t, a.Root, "grok")
	if err = a.Switch(context.Background(), stale.ID, "/fake/grok"); !errors.Is(err, accountnative.ErrNoIdentity) {
		t.Fatal("stale identity switch did not fail", err)
	}
	after, err := a.List()
	if err != nil || len(after) != len(before) {
		t.Fatalf("failed switch saved accounts: %d before, %d after, %v", len(before), len(after), err)
	}
	if left := accountHomes(t, a.Root, "grok"); len(left) != len(homes) {
		t.Fatalf("failed switch left account homes: %v before, %v after", homes, left)
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || !strings.Contains(string(raw), "unsaved-canary") {
		t.Fatal("failed switch did not restore the native login", string(raw), err)
	}
}

func accountHomes(t *testing.T, root, harness string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, harness))
	if err != nil {
		t.Fatal(err)
	}
	var homes []string
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 32 {
			homes = append(homes, e.Name())
		}
	}
	return homes
}

func TestActiveAccountCanSignInAgainBeforeSwitch(t *testing.T) {
	a := testAccounts(t)
	n := signedCodex(t, a, "original", "original-canary")
	if err := a.Switch(context.Background(), n.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(n.Home, "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"replacement","chatgpt_account_id":"workspace"}}`)) + ".signature"
	doc["tokens"].(map[string]any)["id_token"] = jwt
	doc["tokens"].(map[string]any)["refresh_token"] = "replacement-canary"
	raw, _ = json.Marshal(doc)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := a.Capture(context.Background(), n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Switch(context.Background(), n.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	m, err := a.Manager(n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.Capture(context.Background(), "")
	if err != nil || current[0].Identity != refreshed.Identity {
		t.Fatal("saved re-login was overwritten by outgoing native state", err)
	}
	rows, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, row := range rows {
		if row.Identity == n.Identity {
			old, e := readAccountLogin(row.Home)
			if e != nil || !strings.Contains(string(old.Credential.Payload), "original-canary") {
				t.Fatal("outgoing owner was lost", e)
			}
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("outgoing account slot was not preserved")
	}
}

func TestAccountDiscoveryDoesNotCreateStorage(t *testing.T) {
	a := testAccounts(t)
	if rows, err := a.List(); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if _, err := a.Active("codex"); err == nil {
		t.Fatal("missing active selection accepted")
	}
	if _, err := a.HarnessHome("codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Manager("missing", "/fake/native"); err == nil {
		t.Fatal("missing account accepted")
	}
	if pending, running := a.SwitchStatus("codex"); pending || running != 0 {
		t.Fatal(pending, running)
	}
	if _, err := os.Stat(a.Root); !os.IsNotExist(err) {
		t.Fatal("discovery created storage", err)
	}
}

func TestSavedManagerReadsInactiveSnapshotWithoutNativeChanges(t *testing.T) {
	a := testAccounts(t)
	first := signedCodex(t, a, "first", "inactive-native-canary")
	second := signedCodex(t, a, "second", "active-native-canary")
	if err := a.Switch(context.Background(), second.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Manager(first.ID, "/fake/codex"); err == nil {
		t.Fatal("inactive row received runtime manager")
	}
	m, err := a.SavedManager(first.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := m.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 || captured[0].Identity != first.Identity {
		t.Fatal(captured, err)
	}
	if _, err = m.Refresh(context.Background(), captured[0]); !errors.Is(err, accountnative.ErrUnsupported) {
		t.Fatal("saved refresh allowed", err)
	}
	if _, err = m.Activate(context.Background(), "", captured[0].Credential); !errors.Is(err, accountnative.ErrUnsupported) {
		t.Fatal("saved activation allowed", err)
	}
	active, err := a.Active("codex")
	if err != nil || active.ID != second.ID {
		t.Fatal("saved capture changed runtime selection", err)
	}
}

func TestNativeCaptureDuringSkillPublicationAndAuthReplacement(t *testing.T) {
	a := testAccounts(t)
	n := signedCodex(t, a, "fixture", "native-canary")
	if err := a.Switch(context.Background(), n.ID, "/fake/codex"); err != nil {
		t.Fatal(err)
	}
	home, _ := a.HarnessHome("codex")
	auth := filepath.Join(home, "auth.json")
	raw, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := a.Manager(n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() {
		for range 200 {
			stage, err := os.MkdirTemp(home, ".skills-view-")
			if err != nil {
				writer <- err
				return
			}
			if err = os.Symlink("/never-follow-this-staged-link", filepath.Join(stage, "skills")); err != nil {
				writer <- err
				return
			}
			if err = os.RemoveAll(stage); err != nil {
				writer <- err
				return
			}
			f, err := os.CreateTemp(home, ".native-auth-")
			if err != nil {
				writer <- err
				return
			}
			if _, err = f.Write(raw); err != nil {
				f.Close()
				writer <- err
				return
			}
			if err = f.Close(); err != nil {
				writer <- err
				return
			}
			if err = os.Rename(f.Name(), auth); err != nil {
				writer <- err
				return
			}
		}
		writer <- nil
	}()
	var captureErr error
	for range 200 {
		if err = checkAccountFiles("codex", home, true); err != nil {
			captureErr = errors.Join(errors.New("tree permissions"), err)
			break
		}
		captured, err := manager.Capture(context.Background(), "")
		if err != nil {
			captureErr = errors.Join(errors.New("native capture"), err)
			break
		}
		if len(captured) != 1 || captured[0].Identity != n.Identity {
			captureErr = errors.New("native identity changed during configuration publication")
			break
		}
	}
	// Always join the local writer before reporting a failed assertion.
	if err = <-writer; err != nil {
		t.Fatal(err)
	}
	if captureErr != nil {
		t.Fatal(captureErr)
	}
}

func TestMusePrivateLoginUsesNativeAdapter(t *testing.T) {
	for _, useXDG := range []bool{true, false} {
		name := "xdg-config"
		if !useXDG {
			name = "home-fallback"
		}
		t.Run(name, func(t *testing.T) {
			a := testAccounts(t)
			if !useXDG {
				t.Setenv("XDG_CONFIG_HOME", "")
			}
			defer personalAccountCanaries(t)()
			realHome, xdgBefore := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME")
			shared, _ := a.HarnessHome("muse")
			auth := filepath.Join(shared+".home", ".config", "muse", "auth.json")
			launcherOnly := filepath.Join(t.TempDir(), "auth.json")
			launcherBytes := []byte(`{"launcherOnly":"unchanged-fixture"}`)
			if err := os.WriteFile(launcherOnly, launcherBytes, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MUSE_AUTH_PATH", launcherOnly)
			t.Setenv("META_API_KEY", "inherited-fixture-key")
			t.Setenv("TBH_CREDENTIAL_BACKEND", "keychain")
			writeLogin := func(user, canary string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(auth), 0700); err != nil {
					t.Fatal(err)
				}
				doc := map[string]any{"schema_version": 1, "future": map[string]bool{"keep": true},
					"providers": map[string]any{
						"meta":  map[string]string{"mechanism": "oauth", "access_token": canary, "user_email": user + "@example.test"},
						"other": map[string]string{"fixture": "preserve-other-provider"},
					}}
				raw, _ := json.Marshal(doc)
				if err := os.WriteFile(auth, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			writeLogin("original", "muse-original-canary")
			target, err := a.Create("muse", "target")
			if err != nil {
				t.Fatal(err)
			}
			flow, err := a.BeginLogin(context.Background(), target.ID, "/fake/muse")
			if err != nil {
				t.Fatal(err)
			}
			if flow.Warning != "" {
				t.Fatal(flow.Warning)
			}
			env := map[string]string{}
			for _, v := range flow.Env {
				key, value, _ := strings.Cut(v, "=")
				env[key] = value
				if key == "HOME" && value != shared+".home" {
					t.Fatal(key)
				}
			}
			if env["MUSE_AUTH_PATH"] != auth || env["META_API_KEY"] != "" || env["TBH_CREDENTIAL_BACKEND"] != "file" {
				t.Fatal("launcher/native login scope or auth clearing differs")
			}
			if _, _, _, err = a.Login(target.ID, "/fake/muse"); err == nil {
				t.Fatal("ungated shared Muse login allowed")
			}
			if _, err = a.Capture(context.Background(), target.ID, "/fake/muse"); err == nil {
				t.Fatal("ungated shared Muse capture allowed")
			}
			writeLogin("target", "muse-target-canary")
			signed, err := flow.Complete(context.Background())
			if err != nil || !signed.Active || signed.Identity != "meta:email:target@example.test" {
				t.Fatal("native Muse login completion failed", err)
			}
			nestedSkills := filepath.Join(shared+".home", ".config", "muse", "skills")
			generated := filepath.Join(store.DataDir(), "harnesses", "generations", "muse-fixture", "skills")
			for _, dir := range []string{filepath.Dir(nestedSkills), generated} {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(generated, "SKILL.md")
			if err = os.WriteFile(source, []byte("fixture skill"), 0644); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(generated, nestedSkills); err != nil {
				t.Fatal(err)
			}
			rows, err := a.List()
			if err != nil {
				t.Fatal(err)
			}
			var original NativeAccount
			for _, row := range rows {
				if row.Identity == "meta:email:original@example.test" {
					original = row
				}
			}
			if original.ID == "" {
				t.Fatal("outgoing private Muse login was not saved")
			}
			if err = a.Switch(context.Background(), original.ID, "/fake/muse"); err != nil {
				t.Fatal(err)
			}
			manager, err := a.Manager(original.ID, "/fake/muse")
			if err != nil {
				t.Fatal(err)
			}
			captured, err := manager.Capture(context.Background(), "")
			if err != nil || len(captured) != 1 || captured[0].Identity != original.Identity {
				t.Fatal("runtime manager did not read private XDG login", err)
			}
			raw, err := os.ReadFile(auth)
			if err != nil || !strings.Contains(string(raw), "muse-original-canary") || !strings.Contains(string(raw), "preserve-other-provider") {
				t.Fatal("Muse switch lost private login or unrelated native records", err)
			}
			var preserved struct {
				Future struct {
					Keep bool `json:"keep"`
				} `json:"future"`
			}
			if json.Unmarshal(raw, &preserved) != nil || !preserved.Future.Keep {
				t.Fatal("Muse switch lost unrelated native root records")
			}

			// Cancelling another interactive sign-in restores this private login.
			cancelled, err := a.Create("muse", "cancelled")
			if err != nil {
				t.Fatal(err)
			}
			flow, err = a.BeginLogin(context.Background(), cancelled.ID, "/fake/muse")
			if err != nil {
				t.Fatal(err)
			}
			writeLogin("cancelled", "muse-cancelled-canary")
			if err = flow.Cancel(); err != nil {
				t.Fatal(err)
			}
			captured, err = manager.Capture(context.Background(), "")
			if err != nil || len(captured) != 1 || captured[0].Identity != original.Identity {
				t.Fatal("Muse cancellation did not restore private login", err)
			}
			release, err := a.TurnLease(context.Background(), "muse")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			blocked, err := a.BeginLogin(ctx, target.ID, "/fake/muse")
			cancel()
			release()
			if err == nil {
				blocked.Cancel()
				t.Fatal("Muse private login bypassed a live turn")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if os.Getenv("HOME") != realHome || os.Getenv("XDG_CONFIG_HOME") != xdgBefore {
				t.Fatal("personal roots changed")
			}
			untouched, err := os.ReadFile(launcherOnly)
			if err != nil || string(untouched) != string(launcherBytes) {
				t.Fatal("unrelated launcher-only file changed", err)
			}
			raw = accountMetadataBytes(t, a)
			for _, canary := range []string{"muse-original-canary", "muse-target-canary", "muse-cancelled-canary"} {
				if strings.Contains(string(raw), canary) {
					t.Fatal("Muse grant leaked into metadata")
				}
			}
			checkMode(t, auth, 0600)
			checkMode(t, source, 0644)
			// The same target is not permitted under a credential filename.
			if err = os.Remove(auth); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(generated, filepath.Join(filepath.Dir(nestedSkills), "auth.json")); err != nil {
				t.Fatal(err)
			}
			if err = checkPrivateAccountHome("muse", shared, false); err == nil {
				t.Fatal("nested credential-name symlink was allowed")
			}

		})
	}
}

func TestSharedLoginFixtureDoesNotProbeHostGo(t *testing.T) {
	// A shared-login test must not discover the developer's Go toolchain.
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	a, _, first := setupSharedLogin(t, "opencode")
	bin := t.TempDir()
	marker := filepath.Join(bin, "go-probed")
	t.Setenv("GO_PROBE_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf queried > \"$GO_PROBE_MARKER\"\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	flow, err := a.BeginLogin(t.Context(), first.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Cancel()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shared-login fixture launched host Go discovery: %v", err)
	}
	for _, key := range []string{"GOPATH", "GOMODCACHE", "GOCACHE"} {
		want := key + "=" + os.Getenv(key)
		found := false
		for _, value := range flow.Env {
			if value == want {
				found = true
			}
		}
		if os.Getenv(key) == "" || !found {
			t.Errorf("login environment missing explicit %s", key)
		}
	}
}

// A selection that cannot be read for a reason other than its path, such as
// a dyna.db that cannot open, reports the store's error, not a path error.
func TestActiveReportsTheStoreError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dyna.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &Accounts{Root: filepath.Join(dir, "accounts")}
	if _, err := a.activeID("codex"); err == nil || errors.Is(err, errAccountPath) {
		t.Fatalf("activeID with a broken dyna.db = %v, want the store's error", err)
	}
}
