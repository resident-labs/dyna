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

// A PR that another contributor opened from their own fork into the room's
// upstream is tracked read only from the room's fork checkout (D-503).
func TestTrackPRRefAcceptsAnotherForksPRIntoTheRoomsUpstream(t *testing.T) {
	e, rec, root := workspaceEnv(t)
	author := e.start(rec, "author", nil)
	pull := watch.Pull{Number: 3964, Title: "Contributor change", State: "open"}
	pull.Head.SHA = strings.Repeat("b", 40)
	pull.Head.Repo.FullName = "levzzz/horizon"
	prfixture.FakeGHWithPull(t, pull)
	if out, err := exec.Command("git", "-C", root, "remote", "set-url", "origin", "https://github.com/arismoko/horizon.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote set-url: %v %s", err, out)
	}
	if err := e.svc.Notify.Rooms.UpdatePreferences(rec.ID, func(r *room.Record) error {
		r.Upstream = "github.com/horizon-rust/horizon"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref := "https://github.com/horizon-rust/horizon/pull/3964"
	if _, err := e.svc.PRs.TrackPRRef(ownerCtx, rec.ID, author.ID, ref, root, "", nil); err != nil {
		t.Fatalf("a contributor's PR into the room's upstream was refused from the fork checkout: %v", err)
	}
}
