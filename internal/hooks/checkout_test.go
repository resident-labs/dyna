package hooks

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// A clean room checkout on the default branch moves to origin's default
// branch when origin advances, and no commit is made in the checkout.
func TestFastForwardCheckoutMovesCleanDefaultBranch(t *testing.T) {
	origin := t.TempDir()
	checkout := t.TempDir()
	gitIn(t, origin, "init", "-q", "--initial-branch=main")
	gitIn(t, origin, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, "", "clone", "-q", origin, checkout)
	gitIn(t, origin, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "second")
	gitIn(t, checkout, "fetch", "-q", "origin")

	reason, err := fastForwardCheckout(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("clean checkout skipped: %s", reason)
	}
	head := strings.TrimSpace(gitIn(t, checkout, "rev-parse", "HEAD"))
	want := strings.TrimSpace(gitIn(t, origin, "rev-parse", "HEAD"))
	if head != want {
		t.Fatalf("HEAD = %s, want origin/main %s", head, want)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
