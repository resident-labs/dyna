package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func TestLifecycleCLI(t *testing.T) {
	withFakeNodeTools(t)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	serveHooks(t)
	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "initial"}} {
		if _, err := repoid.Git(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		c := NewCommand()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs(append([]string{"--repo", repo}, args...))
		err := c.ExecuteContext(ctx)
		return out.String(), err
	}
	if _, err := run("add", "show", "--event", "after-land", "--command", "printf '%s %s' \"$DYNA_HOOK_EVENT\" \"$DYNA_HOOK_PR\"", "--timeout", "2s"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, `"timeout":"2s"`) || !strings.Contains(out, `"addedBy":"owner"`) {
		t.Fatalf("list: %s %v", out, err)
	}
	if _, err := run("edit", "show", "--timeout", "3s"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, `"timeout":"3s"`) || !strings.Contains(out, `"editedBy":"owner"`) || !strings.Contains(out, `"editedAt":`) || !strings.Contains(out, `"addedBy":"owner"`) {
		t.Fatalf("edit: %s %v", out, err)
	}
	for _, args := range [][]string{{"edit", "show"}, {"edit", "show", "--command", ""}, {"edit", "show", "--timeout", "0s"}, {"edit", "show", "--slot", "invalid"}, {"edit", "missing", "--command", "true"}} {
		if _, err := run(args...); err == nil {
			t.Fatalf("invalid edit accepted: %v", args)
		}
	}
	if out, err := run("test", "show"); err != nil || !strings.Contains(out, "pass") {
		t.Fatalf("test: %s %v", out, err)
	}
	if out, err := run("log", "show"); err != nil || !strings.Contains(out, "after-land 1") {
		t.Fatalf("log: %s %v", out, err)
	}
	if _, err := run("run", "show"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("rm", "show"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("run", "show"); err == nil {
		t.Fatal("unknown hook ran")
	}
	if _, err := run("add", "bad", "--event", "after-land", "--command", "true", "--on-fail", "block"); err == nil {
		t.Fatal("invalid block accepted")
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module dyna-agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "desktop", "build.sh"), []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := run("add", "--recipe", "self-update"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, "main-updated") || !strings.Contains(out, "hooks _install") {
		t.Fatalf("recipe: %s %v", out, err)
	}
	if _, err := run("add", "--recipe", "unknown"); err == nil {
		t.Fatal("unknown recipe accepted")
	}

	if _, err := run("add", "old-event", "--event", "main-moved", "--command", "true"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("list"); err != nil || strings.Contains(out, `"event":"main-moved"`) {
		t.Fatalf("CLI event alias: %s %v", out, err)
	}

}

func TestEveryHooksCommandHasHelp(t *testing.T) {
	root := NewCommand()
	if root.Short == "" || root.Long == "" {
		t.Fatal("hooks help is empty")
	}
	for _, cmd := range root.Commands() {
		if cmd.Short == "" || cmd.Long == "" {
			t.Errorf("%s help is empty", cmd.Name())
		}
	}
}

// withFakeNodeTools appends stub npm and node commands to PATH. The
// self-update recipe resolves go, npm and node when it is added; CI's Go
// container has no Node tools, and the recipe never runs them in tests.
func withFakeNodeTools(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"npm", "node"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
}

func TestHooksRejectRetiredDispatch(t *testing.T) {
	cmd := NewCommand()
	if retired, _, err := cmd.Find([]string{"_dispatch"}); err == nil {
		t.Fatalf("retired hook dispatch command still resolves: %s", retired.CommandPath())
	}
}

// Use the hook service behind a fake engine. CLI writes must cross its socket.
func serveHooks(t *testing.T) {
	t.Helper()
	s := hooks.New()
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "ListHooks", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.List},
		{Name: "HookRuns", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.Runs},
		{Name: "AddHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo string, h hooks.Hook) error {
			h.AddedBy = "owner"
			return s.Add(ctx, repo, h)
		}},
		{Name: "EditHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string, p hooks.Patch) error {
			return s.Edit(ctx, repo, name, "owner", p)
		}},
		{Name: "RemoveHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.Remove},
		{Name: "RunHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string) (hooks.Run, error) {
			return s.Run(ctx, hooks.Event{Repo: repo, Session: "owner"}, name)
		}},
		{Name: "TestHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string) (hooks.Run, error) {
			return s.Run(ctx, hooks.Event{Repo: repo, Session: "owner", Branch: "sample-branch", SHA: strings.Repeat("0", 40), PR: "1"}, name)
		}},
		{Name: "ReconcileHookInstall", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.ReconcileInstall},
	})
	if err != nil {
		t.Fatal(err)
	}
	// This fixture serves the test caller as Owner, as Serve already does.
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, authority.Actor{Role: authority.Owner}), args)
		}
	}
	t.Cleanup(authority.InEngine())
	enginetest.Serve(t, store.DataDir(), calls)
}

func TestHookWritesReturnEngineFailure(t *testing.T) {
	for _, tc := range []struct {
		method string
		args   []string
	}{
		{"AddHook", []string{"add", "audit", "--event", "after-land", "--command", "true"}},
		{"EditHook", []string{"edit", "audit", "--command", "true"}},
		{"RemoveHook", []string{"rm", "audit"}},
		{"RunHook", []string{"run", "audit"}},
		{"TestHook", []string{"test", "audit"}},
		{"ReconcileHookInstall", []string{"log"}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			calls := map[string]websvc.Call{tc.method: func(context.Context, []json.RawMessage) (any, error) {
				return nil, errors.New("write refused by authority")
			}}
			enginetest.Serve(t, t.TempDir(), calls)
			cmd := NewCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "write refused by authority") {
				t.Fatalf("engine failure = %v", err)
			}
			if entries, err := os.ReadDir(store.DataDir()); err == nil && len(entries) != 0 {
				t.Fatalf("CLI wrote local state: %v", entries)
			}
		})
	}
}

func TestHookWriteWithoutEngineFailsOnCancel(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := NewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "audit", "--event", "after-land", "--command", "true"})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("engine unavailable with canceled call = %v", err)
	}
	if entries, err := os.ReadDir(store.DataDir()); err == nil && len(entries) != 0 {
		t.Fatalf("CLI wrote local state: %v", entries)
	}
}

func TestHookRunResolvesRepoBeforeCallingEngine(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	calls, err := websvc.Bind([]websvc.Binding{{Name: "RunHook", Access: websvc.Write, Scope: websvc.ScopeAdmin,
		Method: func(_ context.Context, path, name string) (hooks.Run, error) {
			if path != repo || name != "audit" {
				t.Errorf("engine arguments = %q, %q; want %q, audit", path, name, repo)
			}
			return hooks.Run{ID: "run-1", Name: name, Result: "pass", LogPath: "/engine/hook.log"}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, t.TempDir(), calls)
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetArgs([]string{"run", "audit"})
	cmd.SetOut(&out)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out.String() != "audit pass /engine/hook.log\n" {
		t.Fatalf("run output = %q", out.String())
	}
}

func TestHookReadsReturnEngineFailure(t *testing.T) {
	for _, tc := range []struct {
		method string
		args   []string
	}{
		{"ListHooks", []string{"list"}},
		{"HookRuns", []string{"log", "audit"}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			calls := map[string]websvc.Call{
				"ReconcileHookInstall": func(context.Context, []json.RawMessage) (any, error) { return nil, nil },
				tc.method: func(context.Context, []json.RawMessage) (any, error) {
					return nil, errors.New("engine hook read refused")
				},
			}
			enginetest.Serve(t, t.TempDir(), calls)
			cmd := NewCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "engine hook read refused") {
				t.Fatalf("engine read failure = %v", err)
			}
		})
	}
}
