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
