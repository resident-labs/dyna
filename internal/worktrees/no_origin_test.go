package worktrees

import (
	"context"
	"testing"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

// D-137: a repository made with git init and no remote refused a local base,
// because NewFor always fetched origin and the authenticated fetch needs the
// origin URL. A local base such as HEAD needs no fetch.
func TestNewForAcceptsALocalBaseWithoutAnOriginRemote(t *testing.T) {
	storetest.Isolate(t)
	v := settings.Defaults()
	if err := v.Set(settings.KeyWorktreesRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "test@example.invalid")
	git(t, repo, "config", "user.name", "Test")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	rec, err := NewFor(context.Background(), repo, "integ", "HEAD", "owner", "")
	if err != nil {
		t.Fatalf("worktree from a local base in a repository without origin: %v", err)
	}
	if _, err := gitOutput(context.Background(), rec.Path, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("new worktree has no commit: %v", err)
	}
}
