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
	harnesshome "dyna-agent/internal/harness/home"
)

func TestAccountPrivateManagerAndLoginRoots(t *testing.T) {
	for _, h := range []string{"claude-code", "pi"} {
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
			nativeKeys := map[string]string{"claude-code": "CLAUDE_CONFIG_DIR", "pi": "PI_CODING_AGENT_DIR"}
			if key := nativeKeys[h]; key != "" && env[key] != scope {
				t.Fatalf("native scope changed: %s = %q", key, env[key])
			}
			if strings.Join(before, "\n") != strings.Join(os.Environ(), "\n") {
				t.Fatal("parent environment changed")
			}
		})
	}
}

func TestPrivateHomeKnownPathsAndNativeTreeBoundary(t *testing.T) {
	for _, h := range []string{"kimi-code"} {
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
	if err = checkAccountFiles("pi", scope, true); err != nil {
		t.Fatal("native scan entered paired HOME", err)
	}
}

func TestRemoveDeletesSavedPairedHomeOnly(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("pi", "remove")
	if err != nil {
		t.Fatal(err)
	}
	if err = harnesshome.Ensure(n.Home); err != nil {
		t.Fatal(err)
	}
	if !IsAccountPath(filepath.Join(harnesshome.Home(n.Home), "export-canary")) {
		t.Fatal("paired HOME escaped account export protection")
	}
	scope, _ := a.HarnessHome("pi")
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

func TestNativeFilesManagerUsesHarnessSpecificRoot(t *testing.T) {
	for _, h := range []string{"pi"} {
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
				if _, err := a.Create("pi", "reject"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			case "harness":
				if err := os.MkdirAll(a.Root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(a.Root, "pi")); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Create("pi", "reject"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			case "slot":
				n, err := a.Create("pi", "reject")
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
				if err := os.MkdirAll(filepath.Join(a.Root, "pi"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(a.Root, "pi", ".runtime")); err != nil {
					t.Fatal(err)
				}
				if _, err := a.HarnessHome("pi"); !errors.Is(err, errAccountPath) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFinalAccountManagerCredentialRootGuard(t *testing.T) {
	keys := map[string]string{"claude-code": "CLAUDE_SECURESTORAGE_CONFIG_DIR", "pi": "PI_CODING_AGENT_DIR"}
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
