package worktrees

import (
	"context"
	"testing"
)

// A base such as upstream/main must fetch upstream, not origin, so the new
// worktree starts at the commit upstream holds. Short remote names and fully
// qualified remote-tracking refs name the same remote.
func TestNewFetchesRemoteNamedByBase(t *testing.T) {
	cases := []struct {
		name, remote, base string
	}{
		{"long remote name", "upstream", "upstream/main"},
		{"short remote name", "up", "up/main"},
		{"qualified remote-tracking ref", "upstream", "refs/remotes/upstream/main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gcRepo(t)
			upstream := copyFixture(t, fixtures.gc)
			git(t, upstream, "commit", "--allow-empty", "-m", "upstream ahead")
			ahead := git(t, upstream, "rev-parse", "HEAD")
			git(t, repo, "remote", "add", tc.remote, upstream)
			rec, err := NewFor(context.Background(), repo, "from-"+tc.remote, tc.base, "owner", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := git(t, rec.Path, "rev-parse", "HEAD"); got != ahead {
				t.Fatalf("worktree HEAD = %s, want upstream commit %s", got, ahead)
			}
		})
	}
}
