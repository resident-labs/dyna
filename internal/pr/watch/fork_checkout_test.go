package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/repoid"
)

// A PR opened from a fork has the fork as its head repository. Tracking from the
// fork's checkout must keep that checkout on the new record, and checkout
// discovery must find it afterwards.
func TestTrackingKeepsAForkCheckoutOfThePR(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Repo.FullName = "fork-owner/repo"
	e.write()
	fork, err := repoid.RemoteIdentity("", "https://github.com/fork-owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	original := resolveCheckoutRepo
	t.Cleanup(func() { resolveCheckoutRepo = original })
	resolveCheckoutRepo = func(_ context.Context, dir string) (repoid.Repo, error) {
		return repoid.Repo{ID: fork, Root: dir, Main: dir}, nil
	}
	dir := t.TempDir()
	r, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{"github.com", "owner/repo", 17}, e.author, dir)
	if err != nil || !created {
		t.Fatalf("track = %v, created %v", err, created)
	}
	if r.RepoPath != dir {
		t.Fatalf("tracked RepoPath = %q, want the fork checkout %q", r.RepoPath, dir)
	}
	r.RepoPath = ""
	got, err := e.s.checkoutDir(context.Background(), r)
	if err != nil || got != dir {
		t.Fatalf("checkout discovery = %q, %v; want the fork checkout %q", got, err, dir)
	}
}
