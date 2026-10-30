package hooks

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
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

// A room checkout that cannot move logs its skip once for one reason. Ticks of
// other repositories and later fetches with the same reason stay quiet.
func TestTickLogsCheckoutSkipOnceForSameReason(t *testing.T) {
	s, dirty := setup(t)
	ctx := context.Background()
	origin := t.TempDir()
	git(t, origin, "init", "-b", "main")
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "first")
	git(t, dirty, "remote", "add", "origin", origin)
	git(t, dirty, "fetch", "-q", "origin")
	git(t, dirty, "reset", "-q", "--hard", "origin/main")
	git(t, dirty, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if err := os.WriteFile(filepath.Join(dirty, "local.txt"), []byte("edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()
	git(t, clean, "clone", "-q", origin, ".")
	s.Checkouts = func(context.Context) ([]string, error) { return []string{dirty, clean}, nil }
	s.Fetch = func(_ context.Context, repo string) error {
		git(t, repo, "fetch", "-q", "origin")
		return nil
	}
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	now := time.Now()
	for _, tick := range []struct {
		repo string
		at   time.Time
	}{{dirty, now}, {clean, now}, {dirty, now.Add(5 * time.Minute)}} {
		if err := s.Tick(ctx, tick.repo, tick.at); err != nil {
			t.Fatal(err)
		}
	}
	const skip = "skipped, its tree has local changes"
	if got := strings.Count(logs.String(), skip); got != 1 {
		t.Fatalf("skip logged %d times, want 1:\n%s", got, logs.String())
	}
}

// A room may list its checkout through a symlink. The tick still matches it to
// the resolved checkout, so the clean checkout moves once origin advances.
func TestTickFastForwardsRoomCheckoutThroughSymlink(t *testing.T) {
	s, checkout := setup(t)
	ctx := context.Background()
	origin := t.TempDir()
	git(t, origin, "init", "-b", "main")
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "first")
	git(t, checkout, "remote", "add", "origin", origin)
	git(t, checkout, "fetch", "-q", "origin")
	git(t, checkout, "reset", "-q", "--hard", "origin/main")
	git(t, checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	link := filepath.Join(t.TempDir(), "room-checkout")
	if err := os.Symlink(checkout, link); err != nil {
		t.Fatal(err)
	}
	s.Checkouts = func(context.Context) ([]string, error) { return []string{link}, nil }
	s.Fetch = func(context.Context, string) error {
		git(t, checkout, "fetch", "-q", "origin")
		return nil
	}

	now := time.Now()
	if err := s.Tick(ctx, link, now); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "second")
	want := git(t, origin, "rev-parse", "HEAD")
	if err := s.Tick(ctx, link, now.Add(5*time.Minute)); err != nil {
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
