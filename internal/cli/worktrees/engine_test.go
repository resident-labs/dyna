package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktreesvc"
)

func TestMutationsReportEngineFailure(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		args         []string
	}{
		{"new", "NewWorktree", []string{"new", "feature", "--no-deps"}},
		{"gc", "GCWorktrees", []string{"gc"}},
		{"remove", "RemoveWorktrees", []string{"rm", "missing"}},
		{"prune", "PruneWorktrees", []string{"prune", "--older-than=24h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Chdir(t.TempDir())
			var called atomic.Bool
			enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{tc.method: func(_ context.Context, _ []json.RawMessage) (any, error) {
				called.Store(true)
				return nil, errors.New("engine refused the worktree mutation")
			}})
			cmd := NewCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if !called.Load() || err == nil || !strings.Contains(err.Error(), "engine refused the worktree mutation") {
				t.Fatalf("called = %t, error = %v", called.Load(), err)
			}
		})
	}
}

func TestNewSendsAbsoluteRepoToEngine(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	requests := make(chan []json.RawMessage, 1)
	enginetest.Serve(t, storetest.DataDir(t), map[string]websvc.Call{"NewWorktree": func(_ context.Context, args []json.RawMessage) (any, error) {
		requests <- args
		return map[string]any{"record": map[string]any{"path": "/created/worktree"}}, nil
	}})
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"new", "feature", "--repo", "sub", "--base", "origin/main", "--no-deps"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	var repo string
	if len(got) != 4 {
		t.Fatalf("arguments = %s", got)
	}
	if err := json.Unmarshal(got[0], &repo); err != nil {
		t.Fatal(err)
	}
	if repo != filepath.Join(dir, "sub") || string(got[1]) != `"feature"` || string(got[2]) != `"origin/main"` || string(got[3]) != "true" {
		t.Fatalf("arguments = %s", got)
	}
	if out.String() != "/created/worktree\n" {
		t.Fatalf("output = %q", out.String())
	}
}

// The fake engine runs the same service methods as the engine. Its owner
// identity is explicit, so the test process environment cannot select it.
func serveWorktreeEngine(t *testing.T) {
	t.Helper()
	serveWorktreeEngineAs(t, authority.Actor{Role: authority.Owner})
}

func serveWorktreeEngineAs(t *testing.T, actor authority.Actor) {
	t.Helper()
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "NewWorktree", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, repo, branch, base string, noDeps bool) (worktreesvc.NewResult, error) {
			return worktreesvc.New(ctx, repo, branch, base, noDeps, authority.Of(ctx).Session)
		}},
		{Name: "GCWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.GC},
		{Name: "RemoveWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.Remove},
		{Name: "PruneWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.Prune},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, actor), args)
		}
	}
	enginetest.Serve(t, storetest.DataDir(t), calls)
}

func TestMutationWithoutEngineNamesTheCause(t *testing.T) {
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	engineClient = func() *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "missing.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	cmd := NewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"new", "feature", "--no-deps"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "the worktree was not created") || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("new without engine = %v", err)
	}
}

func TestPartialMutationFailurePreservesOutputAndFails(t *testing.T) {
	for _, tc := range []struct {
		name, method, reply, output string
		args                        []string
	}{
		{"gc", "GCWorktrees", `{"trees":[{"path":"/kept","action":"retained","reason":"busy"}],"failure":"cleanup failed"}`, "retained /kept (busy)", []string{"gc"}},
		{"remove", "RemoveWorktrees", `{"lines":["removed /done"],"failure":"cleanup failed"}`, "removed /done", []string{"remove", "one", "two"}},
		{"prune", "PruneWorktrees", `{"decisions":[{"path":"/kept","action":"skipped","state":"error"}],"failure":"cleanup failed"}`, "skipped /kept (error)", []string{"prune", "--older-than=0s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enginetest.Serve(t, storetest.DataDir(t), map[string]websvc.Call{tc.method: func(context.Context, []json.RawMessage) (any, error) { return json.RawMessage(tc.reply), nil }})
			var out bytes.Buffer
			cmd := NewCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "cleanup failed") || !strings.Contains(out.String(), tc.output) {
				t.Fatalf("output = %q, error = %v", out.String(), err)
			}
		})
	}
}

func TestRemoveResolvesPathsInCallerCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	requests := make(chan []json.RawMessage, 1)
	enginetest.Serve(t, storetest.DataDir(t), map[string]websvc.Call{"RemoveWorktrees": func(_ context.Context, args []json.RawMessage) (any, error) {
		requests <- args
		return worktreesvc.RemoveResult{}, nil
	}})
	cmd := NewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"rm", ".", "../other", "tree-name", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	var names []string
	if len(got) != 2 {
		t.Fatalf("arguments = %s", got)
	}
	if err := json.Unmarshal(got[0], &names); err != nil {
		t.Fatal(err)
	}
	want := []string{dir, filepath.Join(filepath.Dir(dir), "other"), "tree-name"}
	if !slices.Equal(names, want) || string(got[1]) != "true" {
		t.Fatalf("arguments = %s, want names %q and force", got, want)
	}
}

func TestNewWorktreeDoesNotJoinCallingWorkflow(t *testing.T) {
	const runID = "wf_manual-tree"
	serveWorktreeEngineAs(t, authority.Actor{Role: authority.Worker, Run: runID, Agent: 2})
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", runID)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	config := settings.Defaults()
	if err := config.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(); err != nil {
		t.Fatal(err)
	}
	repo := commandRepo(t)
	remote := t.TempDir()
	commandGit(t, remote, "init", "--bare")
	commandGit(t, repo, "remote", "add", "origin", remote)
	commandGit(t, repo, "push", "origin", "HEAD:main")
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"new", "manual-tree", "--repo", repo, "--no-deps"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out.String())
	rec, err := worktrees.Get(path)
	if err != nil || rec.RunID != "" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	actions, err := worktrees.ApplyRunDisposition(runID, worktrees.DispositionRemoveAll)
	if err != nil || len(actions) != 0 {
		t.Fatalf("run disposition = %+v, %v", actions, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("run disposition removed the manual worktree: %v", err)
	}
}
