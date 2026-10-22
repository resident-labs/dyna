package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
	harnesshome "dyna-agent/internal/harness/home"
)

func TestAccountPrivateManagerAndLoginRoots(t *testing.T) {
	for _, h := range []string{"claude-code", "codex", "grok", "pi", "muse", "opencode", "antigravity"} {
		t.Run(h, func(t *testing.T) {
			a := testAccounts(t)
			for key := range harnesshome.Discovery(filepath.Join(a.Root, h, ".runtime")) {
				t.Setenv(key, "/personal-override-canary")
			}
			before := append([]string(nil), os.Environ()...)
			state := &sharedLoginFixture{}
			a.NativeManager = func(_ string, host native.Host) (native.Manager, bool) {
				if !strings.HasSuffix(host.Home, ".home") || host.Getenv("HOME") != host.Home {
					t.Fatal("manager private HOME mismatch")
				}
				for key, value := range harnesshome.Roots(strings.TrimSuffix(host.Home, ".home")) {
					if host.Getenv(key) != value {
						t.Fatalf("manager root %s", key)
					}
				}
				return sharedLoginManager{h, state}, true
			}
			n, err := a.Create(h, "test")
			if err != nil {
				t.Fatal(err)
			}
			flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Cancel()
			scope := n.Home
			if sharedServiceAccount(h) {
				scope, _ = a.HarnessHome(h)
			}
			env := map[string]string{}
			for _, v := range flow.Env {
				k, val, _ := strings.Cut(v, "=")
				env[k] = val
			}
			for k, v := range harnesshome.Roots(scope) {
				if env[k] != v {
					t.Fatalf("child %s = %q, want %q", k, env[k], v)
				}
			}
			for key := range harnesshome.Discovery(scope) {
				if env[key] == "/personal-override-canary" {
					t.Fatalf("inherited %s escaped private scope", key)
				}
			}
			nativeKeys := map[string]string{"claude-code": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME", "grok": "GROK_HOME", "pi": "PI_CODING_AGENT_DIR", "opencode": "OPENCODE_CONFIG_DIR"}
			if key := nativeKeys[h]; key != "" && env[key] != scope {
				t.Fatalf("native scope changed: %s = %q", key, env[key])
			}
			if strings.Join(before, "\n") != strings.Join(os.Environ(), "\n") {
				t.Fatal("parent environment changed")
			}
		})
	}
}

func TestLegacyPrivateSnapshotsNeedFreshLogin(t *testing.T) {
	for _, h := range []string{"muse"} {
		t.Run(h, func(t *testing.T) {
			a, state, n := setupSharedLogin(t, h)
			c := fixtureLogin("legacy")
			if err := writeAccountLogin(n.Home, c); err != nil {
				t.Fatal(err)
			}
			if err := a.transaction(func(s *store.State) error {
				e := s.Find(n.ID)
				e.Identity = c.Identity
				e.NeedsLogin = false
				return nil
			}, true); err != nil {
				t.Fatal(err)
			}
			found, err := a.Find(n.ID)
			if err != nil || found.Status != "not signed in" {
				t.Fatal(found, err)
			}
			if err = a.Switch(context.Background(), n.ID, "/fake/native"); !errors.Is(err, errPrivateSignIn) {
				t.Fatal(err)
			}
			saved, err := a.SavedManager(n.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = saved.Capture(context.Background(), ""); !errors.Is(err, errPrivateSignIn) {
				t.Fatal(err)
			}
			status, err := a.CredentialStatus(context.Background(), n.ID, "/fake/native")
			if err != nil || !status.Due || status.Reason == "" {
				t.Fatal(status, err)
			}
			flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			setSharedLogin(state, fixtureLogin("fresh"))
			signed, err := flow.Complete(context.Background())
			if err != nil || signed.Identity != "fresh" {
				t.Fatal(signed, err)
			}
			if err = privateAccountProven(n.Home, "fresh"); err != nil {
				t.Fatal(err)
			}
			if err = a.Switch(context.Background(), n.ID, "/fake/native"); err != nil {
				t.Fatal(err)
			}
			// A marker cannot make a missing snapshot look signed in.
			if err = os.Remove(filepath.Join(n.Home, ".login.json")); err != nil {
				t.Fatal(err)
			}
			found, err = a.Find(n.ID)
			if err != nil || found.Status != "not signed in" {
				t.Fatal(found, err)
			}
		})
	}
}

func TestPrivateLoginRejectsUnchangedFreshCredentials(t *testing.T) {
	a, state, n := setupSharedLogin(t, "muse")
	setSharedLogin(state, fixtureLogin("unchanged"))
	flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = flow.Complete(context.Background()); !errors.Is(err, errPrivateSignIn) {
		t.Fatal(err)
	}
}
func TestPrivateOriginFailsClosed(t *testing.T) {
	for _, kind := range []string{"malformed", "symlink", "hardlink", "identity"} {
		t.Run(kind, func(t *testing.T) {
			a, _, n := setupSharedLogin(t, "muse")
			_ = a
			marker := filepath.Join(n.Home, ".private-login.json")
			switch kind {
			case "malformed":
				os.WriteFile(marker, []byte("{}"), 0600)
			case "symlink":
				os.Symlink(filepath.Join(t.TempDir(), "absent"), marker)
			case "hardlink":
				source := filepath.Join(t.TempDir(), "source")
				os.WriteFile(source, []byte("{}"), 0600)
				os.Link(source, marker)
			case "identity":
				if err := writePrivateAccountOrigin(n.Home, "different"); err != nil {
					t.Fatal(err)
				}
			}
			if err := privateAccountProven(n.Home, "wanted"); err == nil {
				t.Fatal("unsafe origin accepted")
			}
		})
	}
}
func TestPrivateHomeKnownPathsAndNativeTreeBoundary(t *testing.T) {
	for _, h := range []string{"muse", "opencode", "antigravity"} {
		for _, kind := range []string{"parent", "leaf", "hardlink", "lock"} {
			t.Run(h+"/"+kind, func(t *testing.T) {
				a := testAccounts(t)
				scope, _ := a.HarnessHome(h)
				if err := os.MkdirAll(scope, 0700); err != nil {
					t.Fatal(err)
				}
				if err := harnesshome.Ensure(scope); err != nil {
					t.Fatal(err)
				}
				files := privateAccountFiles(h, scope)
				path := files[0]
				if kind == "lock" {
					path = files[len(files)-1]
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(t.TempDir(), "canary")
				if err := os.WriteFile(source, []byte("untouched"), 0644); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "parent":
					os.RemoveAll(filepath.Dir(path))
					os.Symlink(t.TempDir(), filepath.Dir(path))
				case "hardlink":
					os.Link(source, path)
				default:
					os.Symlink(source, path)
				}
				if _, _, err := a.scopedManager(h, scope, "/fake/native"); err == nil {
					t.Fatal("unsafe path accepted")
				}
				raw, _ := os.ReadFile(source)
				if string(raw) != "untouched" {
					t.Fatal("canary changed")
				}
			})
		}
	}
	scope := filepath.Join(t.TempDir(), "n")
	if err := os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	if err := harnesshome.Ensure(scope); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(harnesshome.Home(scope), "tool.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Symlink(t.TempDir(), filepath.Join(harnesshome.Home(scope), "tool-link")); err != nil {
		t.Fatal(err)
	}
	if err = checkAccountFiles("codex", scope, true); err != nil {
		t.Fatal("native scan entered paired HOME", err)
	}
}

func TestRemoveDeletesSavedPairedHomeOnly(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "remove")
	if err != nil {
		t.Fatal(err)
	}
	if err = harnesshome.Ensure(n.Home); err != nil {
		t.Fatal(err)
	}
	if !IsAccountPath(filepath.Join(harnesshome.Home(n.Home), "export-canary")) {
		t.Fatal("paired HOME escaped account export protection")
	}
	scope, _ := a.HarnessHome("codex")
	os.MkdirAll(scope, 0700)
	harnesshome.Ensure(scope)
	history := filepath.Join(scope, "history")
	os.WriteFile(history, []byte("keep"), 0600)
	os.Symlink(t.TempDir(), filepath.Join(harnesshome.Home(n.Home), "tool"))
	if err = a.Remove(n.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(harnesshome.Home(n.Home)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(harnesshome.Home(scope)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(history); err != nil {
		t.Fatal(err)
	}
}

func personalAccountCanaries(t *testing.T) func() {
	t.Helper()
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(os.Getenv("HOME"), ".config")
	}
	paths := []string{
		filepath.Join(config, "muse", "auth.json"),
		filepath.Join(config, "cursor", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".agents", "skills", "personal", "SKILL.md"),
		filepath.Join(os.Getenv("HOME"), ".cursor", "mcp.json"),
		filepath.Join(os.Getenv("HOME"), ".claude.json"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("personal canary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		t.Helper()
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != "personal canary" {
				t.Fatalf("personal file changed: %s: %v", path, err)
			}
		}
	}
}

func TestNativeFilesManagerUsesHarnessSpecificRoot(t *testing.T) {
	for _, h := range []string{"grok", "pi"} {
		t.Run(h, func(t *testing.T) {
			adapter, err := Adapter(h)
			if err != nil {
				t.Fatal(err)
			}
			m := nativeFilesManager{adapter: adapter, host: native.Host{Env: func(k string) string {
				switch k {
				case "GROK_HOME":
					return "/private/grok"
				case "PI_CODING_AGENT_DIR":
					return "/private/pi"
				}
				return ""
			}}}
			root, err := m.root("")
			if err != nil || root != "/private/"+h {
				t.Fatal(root, err)
			}
		})
	}
}

func TestOpenCodeRebindsOnlyCapturedPrivateDatabaseRows(t *testing.T) {
	a := testAccounts(t)
	scope, _ := a.HarnessHome("opencode")
	if err := os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	if err := harnesshome.Ensure(scope); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(scope, "opencode.db")
	if err := os.WriteFile(database, []byte("private database canary"), 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "opencode")
	script := `#!/bin/sh
if [ "$1 $2" = "auth list" ]; then
 printf '%s\n' '[{"id":"opencode","connections":[{"type":"credential","id":"proven"}]}]'
elif [ "$1 $2 $3 $4" = "auth switch opencode proven" ]; then
 exit 0
else
 exit 1
fi
`
	if err := fsutil.WriteExecutable(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	old, _ := native.For("opencode", native.Host{Home: os.Getenv("HOME"), Executable: exe, Env: func(k string) string {
		if k == "OPENCODE_DB" {
			return database
		}
		return os.Getenv(k)
	}})
	rows, err := old.Capture(context.Background(), "")
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	n, err := a.Create("opencode", "old reference")
	if err != nil {
		t.Fatal(err)
	}
	if err = writeAccountLogin(n.Home, rows[0]); err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(func(s *store.State) error {
		e := s.Find(n.ID)
		e.Identity = rows[0].Identity
		e.Integration = rows[0].Integration
		e.NeedsLogin = false
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if err = a.Switch(context.Background(), n.ID, exe); err != nil {
		t.Fatal(err)
	}
	updated, err := readAccountLogin(n.Home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated.Credential.Payload), harnesshome.Home(scope)) {
		t.Fatal("private reference was not rebound")
	}
	raw, err := os.ReadFile(database)
	if err != nil || string(raw) != "private database canary" {
		t.Fatal("database changed", err)
	}
	// An absent row cannot use its old scope reference.
	rows[0].Identity = "opencode:opencode:absent"
	rows[0].Credential.Payload = []byte(strings.ReplaceAll(string(rows[0].Credential.Payload), "proven", "absent"))
	missing, err := a.Create("opencode", "absent")
	if err != nil {
		t.Fatal(err)
	}
	if err = writeAccountLogin(missing.Home, rows[0]); err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(func(s *store.State) error {
		e := s.Find(missing.ID)
		e.Identity = rows[0].Identity
		e.Integration = rows[0].Integration
		e.NeedsLogin = false
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if err = a.Switch(context.Background(), missing.ID, exe); err == nil || !strings.Contains(err.Error(), "Sign in again") {
		t.Fatal(err)
	}
}

func TestTrustedDataRootDoesNotTrustOwnedSymlinks(t *testing.T) {
	for _, kind := range []string{"accounts", "harness", "slot", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			a := testAccounts(t)
			target := t.TempDir()
			switch kind {
			case "accounts":
				if err := os.MkdirAll(filepath.Dir(a.Root), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, a.Root); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Create("codex", "reject"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			case "harness":
				if err := os.MkdirAll(a.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(a.Root, "codex")); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Create("codex", "reject"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			case "slot":
				n, err := a.Create("codex", "reject")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(n.Home); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(target, n.Home); err != nil {
					t.Fatal(err)
				}
				if _, err = a.Find(n.ID); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			case "runtime":
				if err := os.MkdirAll(filepath.Join(a.Root, "codex"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(a.Root, "codex", ".runtime")); err != nil {
					t.Fatal(err)
				}
				if _, err := a.HarnessHome("codex"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFinalAccountManagerCredentialRootGuard(t *testing.T) {
	keys := map[string]string{"claude-code": "CLAUDE_SECURESTORAGE_CONFIG_DIR", "codex": "CODEX_HOME", "grok": "GROK_HOME", "pi": "PI_CODING_AGENT_DIR", "opencode": "OPENCODE_DB", "muse": "MUSE_AUTH_PATH", "antigravity": "JETSKI_APP_DATA_DIR"}
	for h, key := range keys {
		t.Run(h, func(t *testing.T) {
			a := testAccounts(t)
			scope, _ := a.HarnessHome(h)
			_, env, err := a.scopedManager(h, scope, "/fake/native")
			if err != nil {
				t.Fatal(err)
			}
			if err = validateAccountManagerEnv(h, scope, env); err != nil {
				t.Fatal(err)
			}
			env[key] = filepath.Join(t.TempDir(), "personal")
			if err = validateAccountManagerEnv(h, scope, env); !errors.Is(err, errAccountPath) {
				t.Fatal("active credential root escaped", err)
			}
		})
	}
}
