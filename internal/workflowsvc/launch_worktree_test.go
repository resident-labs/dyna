package workflowsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/worktrees"
)

func launchGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func ageTreeFiles(t *testing.T, paths ...string) {
	t.Helper()
	old := time.Now().Add(-25 * time.Hour)
	for _, path := range paths {
		if err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chtimes(p, old, old)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Cleanup that runs while a launch has staged its folder but not yet recorded
// its coordinator must keep the worktree the starting workflow will use.
func TestCleanupDuringLaunchStagingKeepsWorktree(t *testing.T) {
	storetest.Isolate(t)
	repo := t.TempDir()
	launchGit(t, repo, "init", "-b", "main")
	launchGit(t, repo, "config", "user.email", "test@example.com")
	launchGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launchGit(t, repo, "add", "tracked")
	launchGit(t, repo, "commit", "-m", "base")
	tree := filepath.Join(t.TempDir(), "tree")
	launchGit(t, repo, "worktree", "add", "-b", "staging", tree, "main")
	ageTreeFiles(t, tree, strings.TrimSpace(launchGit(t, tree, "rev-parse", "--absolute-git-dir")))

	var cleanupErr error
	previous := mkdirWorkflowDir
	mkdirWorkflowDir = func(path string, perm os.FileMode) error {
		_, cleanupErr = worktrees.GC(context.Background(), repo, worktrees.GCOptions{})
		return os.MkdirAll(path, perm)
	}
	t.Cleanup(func() { mkdirWorkflowDir = previous })
	_, launchErr := Launch(t.Context(), Options{ScriptSrc: "return 1"}, LaunchOptions{Executable: filepath.Join(t.TempDir(), "missing-dyna")})
	if launchErr == nil {
		t.Fatal("launch of a missing executable succeeded")
	}
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("cleanup removed a worktree during launch staging: %v", err)
	}
}
