package view_test

import (
	"os/exec"
	"strings"
	"testing"

	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
)

// A fork opens a PR on its parent repository, and the room's checkout is that
// fork. pr track accepts the PR only when the room names the parent as its upstream.
func TestTrackPRRefAcceptsAForkCheckoutOfTheRoomsUpstream(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	author := e.start(rec, "author", nil)
	pull := watch.Pull{Number: 7, Title: "Fork change", State: "open"}
	pull.Head.SHA = strings.Repeat("a", 40)
	pull.Head.Repo.FullName = "arismoko/horizon"
	prfixture.FakeGHWithPull(t, pull)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	git("remote", "set-url", "origin", "https://github.com/arismoko/horizon.git")
	ref := "https://github.com/horizon-rust/horizon/pull/7"
	track := func() error {
		_, err := e.svc.PRs.TrackPRRef(ownerCtx, rec.ID, author.ID, ref, root, "", nil)
		return err
	}
	setUpstream := func(upstream string) {
		t.Helper()
		if err := e.svc.Notify.Rooms.UpdatePreferences(rec.ID, func(r *room.Record) error {
			r.Upstream = upstream
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := track(); err == nil {
		t.Fatal("a fork checkout without an upstream setting was accepted")
	}
	setUpstream("github.com/horizon-rust/horizon")
	if err := track(); err != nil {
		t.Fatalf("a fork checkout whose upstream is the PR base was refused: %v", err)
	}
	setUpstream("github.com/other/repo")
	if err := track(); err == nil || !strings.Contains(err.Error(), "not a checkout of github.com/horizon-rust/horizon") {
		t.Fatalf("a fork checkout whose upstream is another repository was accepted: %v", err)
	}
}
