package worktreesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/worktrees"
)

// dyna worktree remove refuses a directory that is an open session's cwd,
// even with --force, the same protection prune has (D-397).
func TestRemovePreservesOpenSessionWorktree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	tree, err := os.MkdirTemp("", "dyna-wt-remove-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tree) })
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", tree}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
	recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{
		"id": "worker", "room": "project", "dir": tree,
	})
	rec := worktrees.Record{Path: tree, Repo: filepath.Join(t.TempDir(), "missing"), Head: git("rev-parse", "HEAD"), Port: 49000, RetainedAt: time.Now().Add(-72 * time.Hour)}
	if err := worktrees.Track(rec); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	result, err := Remove(ctx, []string{tree}, true)
	if err == nil && result.Failure == "" {
		t.Fatalf("remove reported success: %+v", result)
	}
	if len(result.Lines) != 1 || !strings.Contains(result.Lines[0], "retained") {
		t.Fatalf("lines: %+v", result.Lines)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("tree removed: %v", err)
	}
}
