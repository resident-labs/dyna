package worktreesvc

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// A room's upstream applies to its fork checkout only. Another repository
// that the same room uses keeps the default base.
func TestNewUsesRoomUpstreamOnlyForForkCheckout(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	newRepo := func() string {
		dir := t.TempDir()
		git(dir, "init", "-q")
		git(dir, "commit", "--allow-empty", "-qm", "initial")
		return dir
	}
	upstream := newRepo()
	git(upstream, "commit", "--allow-empty", "-qm", "upstream ahead")
	ahead := git(upstream, "rev-parse", "HEAD")
	fork := newRepo()
	git(fork, "remote", "add", "parent", upstream)
	other := newRepo()
	identity, err := repoid.RemoteIdentity(fork, upstream)
	if err != nil {
		t.Fatal(err)
	}
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Repo: git(fork, "rev-parse", "--show-toplevel"), CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rooms.SetUpstream(rec.ID, identity); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner, Room: rec.ID})

	fromFork, err := New(ctx, fork, "from-fork", "", true, "")
	if err != nil || fromFork.Failure != "" {
		t.Fatalf("New fork: %v %q", err, fromFork.Failure)
	}
	if got := git(fromFork.Record.Path, "rev-parse", "HEAD"); got != ahead {
		t.Fatalf("fork worktree HEAD = %s, want upstream commit %s", got, ahead)
	}
	fromOther, err := New(ctx, other, "from-other", "", true, "")
	if err != nil || fromOther.Failure != "" {
		t.Fatalf("New other: %v %q", err, fromOther.Failure)
	}
	if got, want := git(fromOther.Record.Path, "rev-parse", "HEAD"), git(other, "rev-parse", "HEAD"); got != want {
		t.Fatalf("other worktree HEAD = %s, want its own default %s", got, want)
	}
}
