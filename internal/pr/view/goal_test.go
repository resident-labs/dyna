package view_test

import (
	"context"
	"testing"

	"dyna-agent/internal/authority"
	prfixture "dyna-agent/internal/pr/view/testfixture"
)

func TestTrackLinksTheAuthorsGoalOnce(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	author := e.start(rec, "author", nil)
	prfixture.FakeGH(t)
	e.mgr.Executable = ""
	asAuthor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: author.ID, Room: rec.ID})
	info, err := e.mgr.Store.Info(author.ID)
	mine := info.Goal
	if err != nil || mine == "" {
		t.Fatalf("author has no goal: %q %v", mine, err)
	}
	got, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "")
	if err != nil || got.PR.GoalID != mine {
		t.Fatalf("new PR must take the author's goal %s: %+v %v", mine, got.PR, err)
	}
	if err := e.svc.PRs.SetPRGoal(ownerCtx, rec.ID, got.PR.Key, ""); err != nil {
		t.Fatal(err)
	}
	// A retrack never backfills or replaces a link unless a goal is named.
	again, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "")
	if err != nil || again.Created || again.PR.GoalID != "" {
		t.Fatalf("retrack changed the link: %+v %v", again, err)
	}
	if _, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "g999"); err == nil {
		t.Fatal("an unknown goal must be refused")
	}
	linked, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", mine)
	if err != nil || linked.PR.GoalID != mine {
		t.Fatalf("explicit goal must link: %+v %v", linked.PR, err)
	}
	if r, _ := e.svc.PRs.Watcher().Store.Get(got.PR.Key); r.GoalID != mine {
		t.Fatalf("link not stored: %q", r.GoalID)
	}
}
