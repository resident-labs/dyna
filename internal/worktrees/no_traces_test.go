package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/settings"
)

// Dyna must leave no visible traces in a user's repository. Worktree setup and
// a harness session turn add no files such as .mcp.json, .claude/, .codex/ or
// .opencode/, no .gitignore, no Dyna branches, and no commit trailers.
func TestWorktreeSetupAndMockSessionLeaveNoTracesInRepo(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Dyna Test")
	git(t, repo, "config", "user.email", "dyna-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "base.txt")
	git(t, repo, "commit", "-m", "base")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	wt, _, cleanup, err := Create(ctx, repo, settings.WorktreesRetentionChanged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup()
		os.RemoveAll(wt)
	})
	if _, err := harness.RunSessionTurn(ctx, profile.Profile{Name: "m", Harness: profile.HarnessMock}, "work", wt, true,
		harness.JournalOptions{}, harness.SteeringOptions{}, harness.SessionOptions{}); err != nil {
		t.Fatal(err)
	}

	traces := []string{".mcp.json", ".claude", ".claude.json", ".codex", ".opencode", ".cursor", ".gemini", ".dyna", "AGENTS.md", "CLAUDE.md", ".gitignore"}
	for _, dir := range []string{repo, wt} {
		if status := git(t, dir, "status", "--porcelain", "--ignored"); status != "" {
			t.Fatalf("Dyna left changes in %s:\n%s", dir, status)
		}
		for _, trace := range traces {
			if _, err := os.Stat(filepath.Join(dir, trace)); !os.IsNotExist(err) {
				t.Fatalf("Dyna left %s in %s", trace, dir)
			}
		}
	}
	for _, branch := range strings.Fields(git(t, repo, "branch", "--format=%(refname:short)")) {
		if strings.Contains(strings.ToLower(branch), "dyna") {
			t.Fatalf("Dyna branch name in the repo: %s", branch)
		}
	}
	log := strings.ToLower(git(t, repo, "log", "--format=%B"))
	for _, trailer := range []string{"co-authored", "generated with", "dyna:"} {
		if strings.Contains(log, trailer) {
			t.Fatalf("Dyna commit trailer %q in the repo log:\n%s", trailer, log)
		}
	}
}
