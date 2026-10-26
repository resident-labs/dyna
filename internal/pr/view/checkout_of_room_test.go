package view

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dyna-agent/internal/room"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestCheckoutOfRoomAcceptsARegisteredRepository(t *testing.T) {
	main, sidecar, other := initTestRepo(t), initTestRepo(t), initTestRepo(t)
	rec := room.Record{Name: "alpha", Repo: main, ExtraRepos: []string{filepath.Join(t.TempDir(), "gone"), sidecar}}
	ctx := context.Background()
	for _, dir := range []string{main, sidecar} {
		if err := CheckoutOfRoom(ctx, rec, dir); err != nil {
			t.Fatalf("%s refused: %v", dir, err)
		}
	}
	// A registered repository that left the disk fails no check, and an
	// unregistered repository stays refused.
	if err := CheckoutOfRoom(ctx, rec, other); err == nil {
		t.Fatal("an unregistered repository was accepted")
	}
}
