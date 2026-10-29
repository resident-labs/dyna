package worktreesvc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
)

// A worktree-created hook can run a tool from node_modules, so dependency
// restoration must finish before the hook starts.
func TestNewRestoresDependenciesBeforeWorktreeHooks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	order := filepath.Join(t.TempDir(), "order.log")
	appendOrder := func(step string) error {
		f, err := os.OpenFile(order, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(step)
		return errors.Join(err, f.Close())
	}
	// A hook launch is recorded only by the engine process.
	t.Cleanup(authority.InEngine())
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err := hooks.New().Add(ctx, repo, hooks.Hook{Name: "order", Event: hooks.WorktreeCreated, Command: "printf hook >> '" + order + "'", Cwd: "worktree", Timeout: "30s", OnFail: "alert"}); err != nil {
		t.Fatal(err)
	}
	restore := prepareDeps
	t.Cleanup(func() { prepareDeps = restore })
	prepareDeps = func(context.Context, string) (string, error) {
		return "", appendOrder("deps")
	}
	result, err := New(ctx, repo, "feature", "HEAD", false, "")
	if err != nil || result.Failure != "" {
		t.Fatalf("New: %v %q", err, result.Failure)
	}
	got, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "depshook" {
		t.Fatalf("steps ran as %q, want dependencies before the hook", got)
	}
}
