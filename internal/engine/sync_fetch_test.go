package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func TestSyncFetchUsesConfiguredRepositories(t *testing.T) {
	calls := testCalls(t, false)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "untrusted-environment")
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("/usr/bin/git", "init", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	cfg, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(settings.KeyLocalLaneRepos, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	call := calls["SyncFetch"]
	if call == nil {
		t.Fatal("SyncFetch is not registered")
	}
	// Use the real binding and caller wrapper through the fake Unix engine.
	callers := &socketCallers{}
	callers.actForCallers(calls)
	enginetest.Serve(t, store.DataDir(), calls)
	var status lanesync.Status
	if err := engineclient.New("").Call(t.Context(), "SyncFetch", nil, &status); err != nil {
		t.Fatal(err)
	}
	if _, ok := status[repo]; !ok {
		t.Fatalf("configured repository missing: %+v", status)
	}
}

func TestSyncFetchRefusesMissingCallerAndReadOnly(t *testing.T) {
	calls := testCalls(t, false)
	call := calls["SyncFetch"]
	if call == nil {
		t.Fatal("SyncFetch is not registered")
	}
	// Keep the real Unix caller but remove its resolved actor. The engine
	// must refuse rather than use its own owner environment.
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{
		"SyncFetch": func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, authority.Actor{}), args)
		},
	})
	if err := engineclient.New("").Call(t.Context(), "SyncFetch", nil, nil); err == nil || !strings.Contains(err.Error(), "sync fetch") {
		t.Fatalf("missing caller: %v", err)
	}
	call = testCalls(t, true)["SyncFetch"]
	if call == nil {
		t.Fatal("SyncFetch is not registered in read-only mode")
	}
	if _, err := call(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), nil); err == nil {
		t.Fatal("read-only engine accepted fetch")
	}
}

func TestSyncFetchRejectsRemoteCall(t *testing.T) {
	calls := testCalls(t, false)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if _, err := calls["SyncFetch"](ctx, nil); err == nil || !strings.Contains(err.Error(), "local engine socket") {
		t.Fatalf("remote fetch: %v", err)
	}
}
