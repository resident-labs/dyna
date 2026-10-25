package harnessconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func serveSyncEngine(t *testing.T, actor authority.Actor) (string, *atomic.Int32, *harnessruntime.Runtime) {
	t.Helper()
	r := harnessruntime.New()
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{{Name: "SyncCatalogAccount", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
		Method: func(ctx context.Context, kind, harness, account string) (harnessruntime.ConfigSyncResult, error) {
			called.Add(1)
			return r.SyncCatalogAccount(authority.WithActor(ctx, actor), kind, harness, account)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return enginetest.Serve(t, store.DataDir(), calls), &called, r
}

func TestSyncUsesOwnerEnginePower(t *testing.T)   { testSyncEnginePower(t, authority.Owner) }
func TestSyncUsesRootEnginePower(t *testing.T)    { testSyncEnginePower(t, authority.Root) }
func TestSyncUsesSessionEnginePower(t *testing.T) { testSyncEnginePower(t, authority.Session) }
func TestSyncUsesWorkerEnginePower(t *testing.T)  { testSyncEnginePower(t, authority.Worker) }

func testSyncEnginePower(t *testing.T, role authority.Role) {
	t.Helper()
	isolateCatalog(t)
	t.Setenv("DYNA_BIN", "/fake/dyna")
	_, called, r := serveSyncEngine(t, authority.Actor{Role: role, Session: "session", Run: "wf_test"})
	account, err := r.Accounts.Create("pi", "metadata only")
	if err != nil {
		t.Fatal(err)
	}
	home, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(store.ConfigDir(), "skills", "load-dyna-orchestrator", "SKILL.md")
	allowed := role == authority.Owner || role == authority.Root
	for _, kind := range []string{"mcp", "skill"} {
		if err := os.MkdirAll(filepath.Dir(canary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(canary, []byte("no fallback"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		if allowed {
			ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Session})
		} else if role == authority.Worker {
			ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
		}
		// The session case has cleared stamps and no actor in the CLI context.
		out, err := runCatalog(ctx, kind, []string{"sync", "--harness", "pi", "--account", account.ID})
		if allowed {
			var result harnessruntime.ConfigSyncResult
			if err != nil {
				t.Fatalf("allowed sync: %v", err)
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(result.ID, "gen-v1-") || result.Directory == "" {
				t.Fatalf("sync result = %s", out)
			}
			if _, err := os.Stat(filepath.Join(result.Directory, "private-home.json")); err != nil {
				t.Fatal(err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), kind+" sync is owner or root only") || out != "" {
				t.Errorf("refused sync = %v, output = %q", err, out)
			}
			if _, err := os.Stat(canary); err != nil {
				t.Errorf("refusal generated locally: %v", err)
			}
		}
	}
	if called.Load() != 2 {
		t.Errorf("engine calls = %d, want 2", called.Load())
	}
}

func TestSyncEngineFailureHasNoLocalFallback(t *testing.T) {
	isolateCatalog(t)
	socket, called, _ := serveSyncEngine(t, authority.Actor{Role: authority.Owner})
	previous := engineClient
	t.Cleanup(func() { engineClient = previous })
	engineClient = func() *engineclient.Client {
		return engineclient.New(socket+".missing", engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	canary := filepath.Join(store.ConfigDir(), "skills", "load-dyna-orchestrator", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(canary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canary, []byte("no fallback"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mcp", "skill"} {
		out, err := runCatalog(t.Context(), kind, []string{"sync", "--harness", "pi", "--account", "selected"})
		if !errors.Is(err, engineclient.ErrNotRunning) || out != "" {
			t.Errorf("sync engine cause = %v, output = %q", err, out)
		}
	}
	if called.Load() != 0 {
		t.Errorf("unexpected engine calls = %d", called.Load())
	}
	if b, err := os.ReadFile(canary); err != nil || string(b) != "no fallback" {
		t.Errorf("CLI generated locally: %v", err)
	}
}

func TestSyncPayloadOutputAndRequiredFlags(t *testing.T) {
	isolateCatalog(t)
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{{Name: "SyncCatalogAccount", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
		Method: func(kind, harness, account string) (map[string]any, error) {
			called.Add(1)
			if harness != "claude" || account != "explicit-account" || (kind != "mcp" && kind != "skill") {
				t.Errorf("sync payload = %q, %q, %q", kind, harness, account)
			}
			out := map[string]any{"id": "gen-fixture", "directory": "/private/generation", "Env": map[string]string{"TOKEN": "secret-canary"}, "Args": []string{"secret-canary"}}
			if kind == "mcp" {
				out["partialReasons"] = []string{"partial"}
			}
			return out, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	for _, kind := range []string{"mcp", "skill"} {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"sync", "--harness", "claude"}, "sync requires --account with an explicit private account ID"},
			{[]string{"sync", "--account", "explicit-account"}, "sync requires --harness and a private account scope"},
		} {
			_, err := runCatalog(t.Context(), kind, tc.args)
			if err == nil || err.Error() != tc.want {
				t.Errorf("required flags = %v", err)
			}
		}
	}
	if called.Load() != 0 {
		t.Fatal("invalid flags reached engine")
	}
	for _, kind := range []string{"mcp", "skill"} {
		out, err := runCatalog(t.Context(), kind, []string{"sync", "--harness", "claude", "--account", "explicit-account"})
		if err != nil {
			t.Fatal(err)
		}
		want := "{\"id\":\"gen-fixture\",\"directory\":\"/private/generation\"}\n"
		if kind == "mcp" {
			want = "{\"id\":\"gen-fixture\",\"directory\":\"/private/generation\",\"partialReasons\":[\"partial\"]}\n"
		}
		if out != want || strings.Contains(out, "secret-canary") {
			t.Errorf("sync output = %q, want %q", out, want)
		}
	}
	if called.Load() != 2 {
		t.Errorf("engine calls = %d, want 2", called.Load())
	}
}
