package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/repoid"
)

// A PR opened from a fork has the fork as its head repository. The fork's
// checkout must be kept on the tracked record and found by checkout discovery.
func TestForkCheckoutIsKeptForAPRFromAFork(t *testing.T) {
	e := newEnv(t)
	fork, err := repoid.RemoteIdentity("", "https://github.com/arismoko/horizon")
	if err != nil {
		t.Fatal(err)
	}
	original := resolveCheckoutRepo
	t.Cleanup(func() { resolveCheckoutRepo = original })
	resolveCheckoutRepo = func(_ context.Context, dir string) (repoid.Repo, error) {
		return repoid.Repo{ID: fork, Root: dir, Main: dir}, nil
	}
	dir := t.TempDir()
	r := Record{Kind: "github", Host: "github.com", Repo: "horizon-rust/horizon", HeadRepo: "arismoko/horizon", Number: 7, RoomID: e.author.RoomID}
	if got := matchingCheckout(context.Background(), r, dir); got != dir {
		t.Fatalf("tracked RepoPath = %q, want the fork checkout %q", got, dir)
	}
	got, err := e.s.checkoutDir(context.Background(), Record{Kind: "github", Host: "github.com", Repo: "horizon-rust/horizon", HeadRepo: "arismoko/horizon", RoomID: e.author.RoomID, LookupDir: dir})
	if err != nil || got != dir {
		t.Fatalf("checkout discovery = %q, %v; want the fork checkout %q", got, err, dir)
	}
	if other := matchingCheckout(context.Background(), Record{Kind: "github", Host: "github.com", Repo: "horizon-rust/other", HeadRepo: "arismoko/other", Number: 7}, dir); other != "" {
		t.Fatalf("checkout of another repository matched: %q", other)
	}
}
