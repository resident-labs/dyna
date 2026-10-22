package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/computers"
	"dyna-agent/internal/harness/config"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

type launchEnvironmentManager struct{ sharedLoginManager }

func (m launchEnvironmentManager) Capture(ctx context.Context, _ string) ([]native.Captured, error) {
	return m.sharedLoginManager.Capture(ctx, "")
}

func TestDoctorChecksStoredSystemRuntimeAsDynaManaged(t *testing.T) {
	r, _ := runtimeFixture(t, "codex")
	var stored profile.Profile
	if err := json.Unmarshal([]byte(`{"name":"old","harness":"codex","runtime":"system"}`), &stored); err != nil {
		t.Fatal(err)
	}
	doctor := r.Doctor(context.Background(), stored)
	if doctor.Status == "system" || doctor.Home == os.Getenv("HOME") || doctor.Binary == "" {
		t.Fatalf("a stored system runtime must report the Dyna-managed copy: %+v", doctor)
	}
}

func TestDoctorReportsCustomHarnessAsDirect(t *testing.T) {
	doctor := New().Doctor(context.Background(), profile.Profile{Name: "own", Harness: profile.HarnessCustom, Command: []string{"true"}})
	if doctor.Status != "direct" || doctor.Reason == "" {
		t.Fatalf("a custom harness runs its own command: %+v", doctor)
	}
}

func TestComputerSessionBinsReusePreparedToolHome(t *testing.T) {
	r, p := runtimeFixture(t, "codex")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	var bridge, firstPath string
	for _, id := range []string{"first-computer", "second-computer"} {
		p.Env = map[string]string{"DYNA_BIN": "/fake/dyna", "DYNA_SESSION": id}
		selected, _, err := r.Prepare(context.Background(), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		base := append([]string{}, os.Environ()...)
		for key, value := range selected.Env {
			base = append(base, key+"="+value)
		}
		// Production adds the computer environment after private preparation.
		// This makes a new computer bin without publishing another bridge set.
		launch, err := computers.Environment(computers.Meta{SessionID: id}, base)
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, pair := range launch {
			key, value, ok := strings.Cut(pair, "=")
			if ok {
				values[key] = value
			}
		}
		if bridge == "" {
			bridge, firstPath = values["DYNA_BIN"], values["PATH"]
		} else if values["DYNA_BIN"] != bridge || values["PATH"] == firstPath {
			t.Fatal("per-session computer bins changed the shared tool-home digest")
		}
	}
}

// Exercise the final Prepare merge rather than only individual adapter maps.
// A pinned fake binary avoids provider installs and account network requests.
func TestEveryHarnessFinalEnvironmentKeepsCredentialPathsManaged(t *testing.T) {
	for _, h := range []string{"claude-code", "codex", "opencode", "muse", "grok", "pi", "antigravity"} {
		t.Run(h, func(t *testing.T) {
			accounts := testAccounts(t)
			scope, err := accounts.HarnessHome(h)
			if err != nil {
				t.Fatal(err)
			}
			for key := range harnesshome.Discovery(scope) {
				t.Setenv(key, "/personal-override-canary")
			}
			if err = os.MkdirAll(scope, 0700); err != nil {
				t.Fatal(err)
			}
			captured := fixtureLogin("private-launch-identity")
			state := &sharedLoginFixture{current: []native.Captured{captured}}
			accounts.NativeManager = func(h string, _ native.Host) (native.Manager, bool) {
				return launchEnvironmentManager{sharedLoginManager{h, state}}, true
			}
			n, err := accounts.Create(h, "environment fixture")
			if err != nil {
				t.Fatal(err)
			}
			if err = writeAccountLogin(n.Home, captured); err != nil {
				t.Fatal(err)
			}
			if requirePrivateAccount(h) {
				if err = writePrivateAccountOrigin(n.Home, captured.Identity); err != nil {
					t.Fatal(err)
				}
				for _, path := range privateAccountFiles(h, scope)[:1] {
					if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(path, []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = accounts.transaction(func(s *accountstore.State) error {
				e := s.Find(n.ID)
				e.Identity, e.NeedsLogin = captured.Identity, false
				return nil
			}, true); err != nil {
				t.Fatal(err)
			}
			if err = accounts.setActive(h, n.ID); err != nil {
				t.Fatal(err)
			}
			bundles := manager(t)
			dir := filepath.Join(bundles.Root, h, "2.0.0")
			if err = os.MkdirAll(filepath.Join(dir, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			a, _ := Adapter(h)
			for _, name := range append([]string{"harness"}, a.Requires...) {
				if err = os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			hash, err := executableHash(dir, "bin/harness")
			if err != nil {
				t.Fatal(err)
			}
			if err = writeNativeGeneration(filepath.Join(dir, "bundle.json"), bundleManifest{Harness: h, Version: "2.0.0", Executable: "bin/harness", SHA256: hash}); err != nil {
				t.Fatal(err)
			}
			if err = freezeBundle(dir); err != nil {
				t.Fatal(err)
			}
			r := &Runtime{Bundles: bundles, Accounts: accounts, Config: config.NewConfigManager(), Generations: filepath.Join(store.DataDir(), "harnesses", "generations")}
			t.Cleanup(func() { _ = removeBundle(r.Generations) })
			p, _, err := r.Prepare(context.Background(), profile.Profile{Harness: h, Env: map[string]string{"DYNA_BIN": "/fake/dyna"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !privateGenerationRoots(p.Env, scope) {
				t.Fatal("final worker roots are not private")
			}
			for key := range harnesshome.Discovery(scope) {
				if p.Env[key] == "/personal-override-canary" {
					t.Fatalf("personal discovery root survived final merge: %s", key)
				}
			}
			keys := map[string][]string{
				"claude-code": {"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR"},
				"codex":       {"CODEX_HOME", "CODEX_SQLITE_HOME"}, "opencode": {"OPENCODE_CONFIG_DIR", "OPENCODE_DB"},
				"muse": {"MUSE_AUTH_PATH"},
				"grok": {"GROK_HOME"}, "pi": {"PI_CODING_AGENT_DIR"},
			}
			for _, key := range keys[h] {
				path := p.Env[key]
				managed := path != "" && (path == scope || inside(scope, path))
				for _, known := range privateAccountFiles(h, scope) {
					managed = managed || path == known
				}
				// Claude credentials stay in the active account's own home.
				managed = managed || key == pinnedSecretKey && pinsCredentials(h) && pinnedSecretOf(scope, path)
				if !managed {
					t.Fatalf("active credential path %s is outside native scope and known auth files: %s", key, path)
				}
			}
		})
	}
}
func TestRuntimeConstructorsCanonicalizeDataRoot(t *testing.T) {
	testAccounts(t)
	alias := os.Getenv("XDG_DATA_HOME")
	actual := alias + "-actual"
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	r := New()
	if r.Accounts.Root != filepath.Join(actual, "dyna", "accounts") || r.Generations != filepath.Join(actual, "dyna", "harnesses", "generations") {
		t.Fatal("runtime constructors did not canonicalize the same trusted data root")
	}
	if NewBundleManager().Root != filepath.Join(actual, "dyna", "harnesses") {
		t.Fatal("bundle constructor kept the data-root alias")
	}
}
