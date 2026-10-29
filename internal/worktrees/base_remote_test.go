package worktrees

import (
	"context"
	"testing"
)

// A base such as upstream/main must fetch upstream, not origin, so the new
// worktree starts at the commit upstream holds.
func TestNewFetchesRemoteNamedByBase(t *testing.T) {
	repo := gcRepo(t)
	upstream := copyFixture(t, fixtures.gc)
	git(t, upstream, "commit", "--allow-empty", "-m", "upstream ahead")
	ahead := git(t, upstream, "rev-parse", "HEAD")
	git(t, repo, "remote", "add", "upstream", upstream)
	rec, err := NewFor(context.Background(), repo, "from-upstream", "upstream/main", "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, rec.Path, "rev-parse", "HEAD"); got != ahead {
		t.Fatalf("worktree HEAD = %s, want upstream commit %s", got, ahead)
	}
}

// A repository can set worktrees.defaultBase, so a fork starts trees at
// upstream/main when the caller names no base.
func TestNewUsesRepositoryDefaultBase(t *testing.T) {
	repo := gcRepo(t)
	upstream := copyFixture(t, fixtures.gc)
	git(t, upstream, "commit", "--allow-empty", "-m", "upstream ahead")
	ahead := git(t, upstream, "rev-parse", "HEAD")
	git(t, repo, "remote", "add", "upstream", upstream)
	git(t, repo, "config", "worktrees.defaultBase", "upstream/main")
	rec, err := NewFor(context.Background(), repo, "fork-default", "", "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Base != "upstream/main" {
		t.Fatalf("base = %q, want upstream/main", rec.Base)
	}
	if got := git(t, rec.Path, "rev-parse", "HEAD"); got != ahead {
		t.Fatalf("worktree HEAD = %s, want upstream commit %s", got, ahead)
	}
}
