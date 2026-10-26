package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/worktrees"
)

// repoWithOrigin creates a repository with main pushed to a local origin, so a
// session worktree can branch from the repository's main as a real one does.
func repoWithOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", origin)
	runGit(t, "", "init", repo)
	runGit(t, repo, "config", "user.name", "Dyna Test")
	runGit(t, repo, "config", "user.email", "dyna-test@example.com")
	write := filepath.Join(repo, "README.md")
	if err := os.WriteFile(write, []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "remote", "add", "origin", origin)
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, "", "--git-dir="+origin, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, repo, "remote", "set-head", "origin", "-a")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	argv := args
	if dir != "" {
		argv = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", argv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A start without --dir works in its own branch worktree, not the parent's
// checkout. --dir stays an explicit exception for a tree the caller chose.
func TestStartGivesEachSessionItsOwnWorktree(t *testing.T) {
	e := newTestEnv(t)
	repo := repoWithOrigin(t)
	info, _ := e.start("RESPOND: ok", func(o *StartOptions) { o.Dir = ""; o.Cwd = repo })
	if info.Dir == repo {
		t.Fatalf("start used the shared checkout %s", repo)
	}
	if !strings.Contains(info.Dir, "dyna") {
		t.Fatalf("start directory %q is not a managed worktree", info.Dir)
	}
	root, err := worktrees.ManagedRoot()
	if err != nil || !strings.HasPrefix(info.Dir, root+string(filepath.Separator)) {
		t.Fatalf("start directory %q is not under the managed root %q: %v", info.Dir, root, err)
	}
	if branches := runGitOutput(t, repo, "branch", "--list", "dyna/*"); !strings.Contains(branches, "dyna/") {
		t.Fatalf("no session branch was created: %q", branches)
	}

	pinned, _ := e.start("RESPOND: ok", func(o *StartOptions) { o.Dir = repo; o.Cwd = repo })
	if pinned.Dir != repo {
		t.Fatalf("--dir used %q instead of the given %q", pinned.Dir, repo)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// A start that fails after creating its tree leaves no worktree or registry
// row behind, so a bad goal reference cannot orphan a checkout.
func TestStartRemovesWorktreeWhenGoalFails(t *testing.T) {
	e := newTestEnv(t)
	repo := repoWithOrigin(t)
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "g999999", Profile: "mock", Cwd: repo, Task: "Do the work."}); err == nil {
		t.Fatal("start with a missing goal succeeded")
	}
	trees, err := worktrees.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 0 {
		t.Fatalf("failed start left worktrees: %+v", trees)
	}
}
