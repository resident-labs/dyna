package hooks

import (
	"context"
	"testing"
	"time"
)

// A room checkout with no hooks still follows its default branch: its tick
// fetches origin, and the clean checkout moves once origin advances.
func TestTickFastForwardsRoomCheckoutWithoutHooks(t *testing.T) {
	s, checkout := setup(t)
	ctx := context.Background()
	origin := t.TempDir()
	git(t, origin, "init", "-b", "main")
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "first")
	git(t, checkout, "remote", "add", "origin", origin)
	git(t, checkout, "fetch", "-q", "origin")
	git(t, checkout, "reset", "-q", "--hard", "origin/main")
	git(t, checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	s.Checkouts = func(context.Context) ([]string, error) { return []string{checkout}, nil }
	s.Fetch = func(context.Context, string) error {
		git(t, checkout, "fetch", "-q", "origin")
		return nil
	}

	now := time.Now()
	if err := s.Tick(ctx, checkout, now); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "second")
	want := git(t, origin, "rev-parse", "HEAD")
	if err := s.Tick(ctx, checkout, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := git(t, checkout, "rev-parse", "HEAD"); got != want {
		t.Fatalf("checkout HEAD = %s, want origin/main %s", got, want)
	}
}

// Separate clones of one remote share a repository ID. Each clone must fetch its
// own origin, so the first clone's fetch does not stand in for the second's.
func TestTickFetchesEachCloneOfSameRemote(t *testing.T) {
	s, first := setup(t)
	ctx := context.Background()
	origin := t.TempDir()
	git(t, origin, "init", "-b", "main")
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "first")
	git(t, first, "remote", "add", "origin", origin)
	git(t, first, "fetch", "-q", "origin")
	git(t, first, "reset", "-q", "--hard", "origin/main")
	git(t, first, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	second := t.TempDir()
	git(t, second, "clone", "-q", origin, ".")
	s.Checkouts = func(context.Context) ([]string, error) { return []string{first, second}, nil }
	s.Fetch = func(_ context.Context, repo string) error {
		git(t, repo, "fetch", "-q", "origin")
		return nil
	}

	now := time.Now()
	if err := s.Tick(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "second")
	want := git(t, origin, "rev-parse", "HEAD")
	if err := s.Tick(ctx, second, now); err != nil {
		t.Fatal(err)
	}
	if got := git(t, second, "rev-parse", "HEAD"); got != want {
		t.Fatalf("second clone HEAD = %s, want origin/main %s", got, want)
	}
}
