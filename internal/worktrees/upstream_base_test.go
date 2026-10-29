package worktrees

import (
	"context"
	"testing"

	"dyna-agent/internal/repoid"
)

// A room with an upstream starts new worktrees from that upstream's default
// branch. The remote is found by repository identity, not by name.
func TestUpstreamBaseStartsFromRemoteDefaultBranch(t *testing.T) {
	ctx := context.Background()
	repo := gcRepo(t)
	upstream := copyFixture(t, fixtures.gc)
	git(t, upstream, "commit", "--allow-empty", "-m", "upstream ahead")
	ahead := git(t, upstream, "rev-parse", "HEAD")
	git(t, repo, "remote", "add", "parent", upstream)
	identity, err := repoid.RemoteIdentity(repo, upstream)
	if err != nil {
		t.Fatal(err)
	}
	base, err := UpstreamBase(ctx, repo, identity)
	if err != nil {
		t.Fatal(err)
	}
	if base != "parent/main" {
		t.Fatalf("base = %q, want parent/main", base)
	}
	rec, err := NewFor(ctx, repo, "from-parent", base, "owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, rec.Path, "rev-parse", "HEAD"); got != ahead {
		t.Fatalf("worktree HEAD = %s, want upstream commit %s", got, ahead)
	}
}
