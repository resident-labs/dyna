// Package worktreetest gives tests a real git worktree that the registry tracks.
package worktreetest

import (
	"os"
	"os/exec"
	"testing"

	"dyna-agent/internal/worktrees"
)

// Tracked returns the path of a git worktree in the temp dir that the registry tracks.
// The caller sets XDG_DATA_HOME first, so the registry lives where the test expects.
func Tracked(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "-c", "user.name=Dyna Test", "-c", "user.email=dyna-test@example.com", "commit", "--allow-empty", "-m", "base")
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wt) })
	git(t, repo, "worktree", "add", "--detach", wt, "HEAD")
	if err := worktrees.Track(worktrees.Record{Path: wt}); err != nil {
		t.Fatal(err)
	}
	return wt
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
