package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/fsutil"
	harnessmgr "dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife/recordtest"
	defaultstore "dyna-agent/internal/store"
)

func runtimeFixture(t *testing.T, h string) (*Runtime, profile.Profile) {
	t.Helper()
	accounts := testAccounts(t)
	var n NativeAccount
	p := profile.Profile{Name: "private", Harness: h}
	if h == "pi" {
		// Pi keeps one account per provider, and its model names the provider.
		n = signedPiAccount(t, &Runtime{Accounts: accounts}, store.ProviderZAI, "fixture-key")
		p.Model = "zai/glm-5.3"
	} else {
		var err error
		n, err = accounts.Create(h, "private")
		if err != nil {
			t.Fatal(err)
		}
		writeAccountFixture(t, n)
		n, err = accounts.Capture(context.Background(), n.ID, "/fake/native")
		if err != nil {
			t.Fatal(err)
		}
	}
	bundles := manager(t)
	bundle := install(t, bundles, h, fixtureVersion(h))
	if err := accounts.Switch(context.Background(), n.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Bundles: bundles, Accounts: accounts, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(defaultstore.DataDir(), "harnesses", "generations")}
	return r, p
}

// fixtureVersion is the oldest version the harness's verified recipe accepts.
func fixtureVersion(h string) string {
	if h == "pi" {
		return piForkVersion
	}
	return "1.0.0"
}
func TestPreparePrivateProfilePinsGenerationAndIsolatesHome(t *testing.T) {
	for _, h := range []string{"claude-code", "pi"} {
		t.Run(h, func(t *testing.T) {
			r, p := runtimeFixture(t, h)
			if h == "claude-code" {
				p.ExtraArgs = []string{"--disallowedTools", "Agent"}
			}
			home, xdg := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME")
			t.Setenv("MCP_TOKEN", "generation-secret-canary")
			if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "tools", MCPDefinition: harnessmgr.MCPDefinition{Command: "node", EnvKeys: []string{"MCP_TOKEN"}}, EnabledHarnesses: []string{h}}); err != nil {
				t.Fatal(err)
			}
			selected, pin, err := r.Prepare(context.Background(), p, nil)
			if err != nil {
				t.Fatal(err)
			}
			if selected.RuntimePermissionArgs == nil || strings.Join(selected.RuntimePermissionArgs, "\x00") != strings.Join(p.ExtraArgs, "\x00") {
				t.Fatal("generated arguments changed the saved user permission arguments")
			}
			if selected.RuntimeExecutable != pin.Bundle.Executable || selected.RuntimeExecutable == "" {
				t.Fatal("private executable was not selected")
			}
			if selected.Env["MCP_TOKEN"] != "generation-secret-canary" {
				t.Fatal("MCP reference was not resolved")
			}
			nativeHome, _ := r.Accounts.HarnessHome(h)
			if !privateGenerationRoots(selected.Env, nativeHome) {
				t.Fatal("worker HOME or XDG roots are not private")
			}
			if selected.Env["HOME"] == home || selected.Env["XDG_CONFIG_HOME"] == xdg {
				t.Fatal("worker still uses owner roots")
			}
			if bridge := selected.Env["DYNA_BIN"]; !inside(harnesshome.Home(nativeHome), bridge) || filepath.Base(bridge) != "dyna" {
				t.Fatal("Dyna commands bypass the owner-store bridge")
			}
			raw, _ := json.Marshal(pin)
			if strings.Contains(string(raw), "generation-secret-canary") || strings.Contains(strings.Join(selected.ExtraArgs, " "), "generation-secret-canary") {
				t.Fatal("secret escaped into pin or arguments")
			}
			if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "later", MCPDefinition: harnessmgr.MCPDefinition{Command: "later"}, EnabledHarnesses: []string{h}}); err != nil {
				t.Fatal(err)
			}
			resumed, next, err := r.Prepare(context.Background(), p, &pin)
			if err != nil {
				t.Fatal(err)
			}
			if next.Generation != pin.Generation || strings.Join(resumed.ExtraArgs, "\x00") != strings.Join(selected.ExtraArgs, "\x00") || strings.Join(resumed.RuntimePermissionArgs, "\x00") != strings.Join(selected.RuntimePermissionArgs, "\x00") {
				t.Fatal("resume changed generation")
			}
			if os.Getenv("HOME") != home || os.Getenv("XDG_CONFIG_HOME") != xdg {
				t.Fatal("host environment changed")
			}
		})
	}
}

// Pi adds its own links inside the account home between turns. A second turn
// must keep preparing, and a regeneration must still publish its views.
func TestPiPrepareRepeatsWithHarnessLinks(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	_, first, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	activeID, err := (&store.Store{Root: r.Accounts.Root}).Active(store.ProviderZAI, "pi", "")
	if err != nil {
		t.Fatal(err)
	}
	account, err := r.Accounts.Find(activeID)
	if err != nil {
		t.Fatal(err)
	}
	// Pi reads its whole agent directory from the account home, and adds its
	// own files there.
	home := account.Home
	arg0 := filepath.Join(home, "tmp", "arg0-linux", "pi-arg0fixture")
	if err = os.MkdirAll(arg0, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(first.Bundle.Executable, filepath.Join(arg0, "apply_patch")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Prepare(context.Background(), p, &first); err != nil {
		t.Fatal("second turn:", err)
	}
	if err = r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedMCP{Name: "later", MCPDefinition: harnessmgr.MCPDefinition{Command: "later"}, EnabledHarnesses: []string{"pi"}}); err != nil {
		t.Fatal(err)
	}
	if err = r.Regenerate("pi"); err != nil {
		t.Fatal(err)
	}
	_, next, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal("turn after regeneration:", err)
	}
	if next.Generation == first.Generation {
		t.Fatal("regeneration kept the old generation")
	}
}
func TestSameAccountAllowsConcurrentPrepare(t *testing.T) {
	for _, h := range []string{"claude-code", "pi"} {
		t.Run(h, func(t *testing.T) {
			r, p := runtimeFixture(t, h)
			active, err := r.Accounts.Active(ProviderForModel(p.Harness, p.Model), p.Harness)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					_, pin, err := r.Prepare(context.Background(), p, nil)
					if err != nil {
						t.Error(err)
						return
					}
					if pin.AccountID != active.ID {
						t.Error("wrong account")
					}
				})
			}
			wg.Wait()
		})
	}
}
func TestMissingPrivateLoginAndProtectedEnvironment(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	p.Env = map[string]string{"HOME": "/another-home"}
	if _, _, err := r.Prepare(context.Background(), p, nil); err == nil {
		t.Fatal("accepted HOME override")
	}
	p.Env = nil
	n, err := r.Accounts.Active(ProviderForModel(p.Harness, p.Model), p.Harness)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(n.Home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Prepare(context.Background(), p, nil); err == nil {
		t.Fatal("launched unsigned account")
	}
	if got := r.Doctor(context.Background(), p); got.Status != "not signed in" {
		t.Fatalf("wrong status: %+v", got)
	}
}

func TestPrivateRuntimeLaunchLetsDynaRestoreOwnerRoots(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	noInstalledDyna(t)
	ownerHome, ownerConfig, ownerData := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME")
	realDyna := filepath.Join(t.TempDir(), "real dyna")
	p.Env = map[string]string{"DYNA_BIN": realDyna, "DYNA_ROOM": "owner-room"}
	selected, _, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Every dyna process restores the owner roots from its launch environment.
	for key, value := range selected.Env {
		t.Setenv(key, value)
	}
	if err := harnesshome.RestoreOwner([]string{"room", "log"}); err != nil {
		t.Fatal(err)
	}
	got := []string{os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME"), os.Getenv("DYNA_ROOM"), os.Getenv("DYNA_BIN")}
	want := []string{ownerHome, ownerConfig, ownerData, "owner-room", realDyna}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dyna lost owner store roots or room stamps: got %q want %q", got, want)
	}
}

func TestDoctorReportsPrivateHomeAndRemainingLimits(t *testing.T) {
	// Doctor reads the active account of the harness, which resolves for
	// Claude Code; a Pi account is selected per provider (see the known gap).
	r, p := runtimeFixture(t, "claude-code")
	nativeHome, _ := r.Accounts.HarnessHome("claude-code")
	doctor := r.Doctor(context.Background(), p)
	if doctor.Home != harnesshome.Home(nativeHome) || doctor.Status != "ready" || doctor.Reason != "" {
		t.Fatalf("unexpected private doctor state: %+v", doctor)
	}
	// Claude Code keeps no skill state in its generation, so it stays ready.
	for _, c := range Capabilities() {
		if c.Harness == "claude-code" && c.Status != "ready" {
			t.Fatalf("claude-code capability = %+v", c)
		}
	}
	warnings := strings.Join(doctor.Warnings, "\n")
	if !strings.Contains(warnings, filepath.Join(doctor.Home, ".cache")) || !strings.Contains(warnings, "toolHomeBridges") {
		t.Fatalf("doctor omits private cache or tool access policy: %s", warnings)
	}
	if !strings.Contains(warnings, "refresh-token family") || !strings.Contains(warnings, "personal login") {
		t.Fatalf("doctor omits the one-time import refresh effect: %s", warnings)
	}
	for _, capability := range Capabilities() {
		if capability.SharedPersonalLogin {
			t.Fatalf("incorrect shared-login warning for %s", capability.Harness)
		}
		if capability.Status != "ready" && capability.Reason == "" {
			t.Fatalf("%s has no reason for its remaining limit", capability.Harness)
		}
	}
}

func TestPrivateRuntimeOwnerToolPolicyOverridesProfile(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	ownerHome := os.Getenv("HOME")
	tools := t.TempDir()
	if err := fsutil.WriteExecutable(filepath.Join(tools, "owner-helper"), []byte("#!/bin/sh\nprintf '%s' \"$HOME\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.Config.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := defaultstore.WriteJSON(r.Config.Path, harnessmgr.HarnessConfig{ToolHomeBridges: []string{"owner-helper"}}, 0600); err != nil {
		t.Fatal(err)
	}
	p.Env = map[string]string{
		"DYNA_BIN":        "/fake/dyna",
		"PATH":            tools + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DYNA_HOME_TOOLS": "untrusted-missing-tool",
	}
	selected, _, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal("profile tool policy overrode the owner JSON policy", err)
	}
	if selected.Env["DYNA_HOME_TOOLS"] != "owner-helper" {
		t.Fatal("final environment did not retain the owner policy")
	}
	bridge := filepath.Join(filepath.Dir(selected.Env["DYNA_BIN"]), "owner-helper")
	cmd := exec.Command(bridge)
	for key, value := range selected.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if raw, err := cmd.Output(); err != nil || string(raw) != ownerHome {
		t.Fatal("owner-selected tool did not receive owner HOME", err)
	}
	doctor := r.Doctor(context.Background(), p)
	if !strings.Contains(strings.Join(doctor.Warnings, "\n"), "owner-helper") {
		t.Fatal("doctor omitted the configured owner HOME bridge")
	}
}
func TestSessionPinsProtectOnlyOpenBundles(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	_, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(defaultstore.DataDir(), "sessions")
	recordtest.Write(t, root, "open", map[string]any{"runtimePin": pin})
	got, err := OpenPins()
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	recordtest.Write(t, root, "open", map[string]any{"runtimePin": pin, "closed": time.Now()})
	got, err = OpenPins()
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

func TestResumeUsesCurrentActiveAccountAndSharedHistoryHome(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	selected, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := signedPiAccount(t, r, store.ProviderZAI, "second-key")
	if err := r.Accounts.Switch(context.Background(), next.ID, pin.Bundle.Executable); err != nil {
		t.Fatal(err)
	}
	resumed, newPin, err := r.Prepare(context.Background(), p, &pin)
	if err != nil {
		t.Fatal(err)
	}
	if newPin.AccountID != next.ID {
		t.Fatal("resume retained the old account")
	}
	if selected.Env[piSessionDirKey] != resumed.Env[piSessionDirKey] {
		t.Fatal("resume lost the shared session history home")
	}
	if resumed.Env[piAgentDirKey] != next.Home {
		t.Fatal("resume did not follow the active account's home")
	}
	if newPin.Generation != pin.Generation {
		t.Fatal("account swap unexpectedly changed immutable config generation")
	}
}

func TestPrepareNeverCopiesPersonalLogin(t *testing.T) {
	accounts := testAccounts(t)
	ownerImportSeed(t, os.Getenv("HOME"), "claude-code")
	bundles := manager(t)
	install(t, bundles, "claude-code", "1.0.0")
	r := &Runtime{Accounts: accounts, Bundles: bundles, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(defaultstore.DataDir(), "harnesses", "generations")}
	_, _, err := r.Prepare(context.Background(), profile.Profile{Harness: "claude-code"}, nil)
	if err == nil || err.Error() != "Sign in to claude-code in Dyna." {
		t.Fatalf("got %v, want the sign-in message", err)
	}
	if list, _ := accounts.List(); len(list) != 0 {
		t.Fatalf("prepare copied the personal login: %+v", list)
	}
	// The user's explicit choice is the only way the personal login gets in.
	if _, err := r.UsePersonalLogin(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Prepare(context.Background(), profile.Profile{Harness: "claude-code"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareIgnoresStoredSystemRuntime(t *testing.T) {
	// This test covers stored profile selection. Owner-login import and
	// first-use installation have separate integration coverage.
	r, _ := runtimeFixture(t, "pi")
	var stored profile.Profile
	if err := json.Unmarshal([]byte(`{"name":"old","harness":"pi","model":"zai/glm-5.3","runtime":"system","runtimeVersion":"`+piForkVersion+`"}`), &stored); err != nil {
		t.Fatal(err)
	}
	selected, pin, err := r.Prepare(context.Background(), stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Bundle.Version != piForkVersion || selected.RuntimeExecutable != pin.Bundle.Executable {
		t.Fatalf("a stored system runtime must run Dyna-managed: %#v", pin.Bundle)
	}
}

func TestPrepareInstallsMissingPrivateCopyOnFirstUse(t *testing.T) {
	accounts := testAccounts(t)
	ownerImportSeed(t, os.Getenv("HOME"), "claude-code")
	bundles := manager(t)
	r := &Runtime{Accounts: accounts, Bundles: bundles, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(defaultstore.DataDir(), "harnesses", "generations")}
	if err := r.copyPersonalLogin(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	selected, pin, err := r.Prepare(context.Background(), profile.Profile{Harness: "claude-code", RuntimeVersion: "1.0.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Bundle.Version != "1.0.0" || selected.RuntimeExecutable != pin.Bundle.Executable {
		t.Fatalf("prepare did not install and select the private copy: %#v", pin.Bundle)
	}
}

// signedPiAccount makes a signed-in Pi account of one provider. Pi stores one
// provider entry per account home.
func signedPiAccount(t *testing.T, r *Runtime, provider store.Provider, key string) NativeAccount {
	t.Helper()
	account, err := r.Accounts.CreateProvider("pi", provider, store.KindAPIKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{string(provider): map[string]string{"type": "api_key", "key": key}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(account.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	account, err = r.Accounts.Capture(context.Background(), account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Accounts.setActiveAccount(account); err != nil {
		t.Fatal(err)
	}
	return account
}

// A Pi turn runs from the home of the active account of the provider its model
// names. A resume keeps the shared session history and the generation, and only
// the credential home follows the account.
func TestPiPrepareUsesTheActiveProviderAccountHome(t *testing.T) {
	r := &Runtime{Bundles: manager(t), Accounts: testAccounts(t), Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(defaultstore.DataDir(), "harnesses", "generations")}
	install(t, r.Bundles, "pi", "1.4.0-dyna.2")
	shared, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	first := signedPiAccount(t, r, store.ProviderZAI, "first")
	p := profile.Profile{Name: "pi-private", Harness: "pi", Model: "zai/glm-5.3"}
	selected, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pin.AccountID != first.ID || selected.Env[piAgentDirKey] != first.Home {
		t.Fatalf("first turn ran from %q on account %q", selected.Env[piAgentDirKey], pin.AccountID)
	}
	if selected.Env[piSessionDirKey] != filepath.Join(shared, "sessions") {
		t.Fatalf("session history is not shared: %q", selected.Env[piSessionDirKey])
	}
	second := signedPiAccount(t, r, store.ProviderZAI, "second")
	if second.Home == first.Home {
		t.Fatal("two accounts of one provider share a home")
	}
	resumed, next, err := r.Prepare(context.Background(), p, &pin)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccountID != second.ID || resumed.Env[piAgentDirKey] != second.Home {
		t.Fatalf("resumed turn ran from %q on account %q", resumed.Env[piAgentDirKey], next.AccountID)
	}
	if resumed.Env[piSessionDirKey] != selected.Env[piSessionDirKey] || !reflect.DeepEqual(resumed.ExtraArgs, selected.ExtraArgs) {
		t.Fatalf("the account changed the shared session history: %q / %v", resumed.Env[piSessionDirKey], resumed.ExtraArgs)
	}
	if next.Generation != pin.Generation {
		t.Fatal("the account changed the immutable config generation")
	}
	// The account home holds the Dyna-managed part of Pi's agent directory, so
	// the turn keeps its MCP servers and model definitions.
	for _, name := range []string{"settings.json", "mcp.json"} {
		if _, err = os.Lstat(filepath.Join(second.Home, name)); err != nil {
			t.Fatalf("the account home lacks %s: %v", name, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(second.Home, "auth.json")); err != nil || !strings.Contains(string(raw), "second") {
		t.Fatalf("the account lost its own login: %s %v", raw, err)
	}
}
