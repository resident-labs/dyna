package view_test

import (
	"context"
	"strings"
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
	got, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "", nil)
	if err != nil || got.PR.GoalID != mine {
		t.Fatalf("new PR must take the author's goal %s: %+v %v", mine, got.PR, err)
	}
	if detail, err := e.svc.PRs.ChangeDetail(rec.ID, got.PR.Key); err != nil || detail.GoalID != mine {
		t.Fatalf("goal link missing from change detail: %+v %v", detail, err)
	}
	if err := e.svc.PRs.SetPRGoal(ownerCtx, rec.ID, got.PR.Key, ""); err != nil {
		t.Fatal(err)
	}
	if detail, err := e.svc.PRs.ChangeDetail(rec.ID, got.PR.Key); err != nil || detail.GoalID != "" {
		t.Fatalf("cleared link remained in cached detail: %+v %v", detail, err)
	}
	// A retrack never backfills or replaces a link unless a goal is named.
	again, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "", nil)
	if err != nil || again.Created || again.PR.GoalID != "" {
		t.Fatalf("retrack changed the link: %+v %v", again, err)
	}
	if _, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", "g999", nil); err == nil {
		t.Fatal("an unknown goal must be refused")
	}
	linked, err := e.svc.PRs.TrackPRRef(asAuthor, rec.ID, author.ID, "7", "", mine, nil)
	if err != nil || linked.PR.GoalID != mine {
		t.Fatalf("explicit goal must link: %+v %v", linked.PR, err)
	}
	if page, err := e.svc.PRs.Changes(rec.ID, "", 0); err != nil || len(page.Items) != 1 || page.Items[0].GoalID != mine {
		t.Fatalf("relinked goal missing from cached list: %+v %v", page, err)
	}
	if r, _ := e.svc.PRs.Watcher().Store.Get(got.PR.Key); r.GoalID != mine {
		t.Fatalf("link not stored: %q", r.GoalID)
	}
}

func TestGoalLinkRequiresTheCurrentAuthor(t *testing.T) {
	for _, method := range []string{"track", "set"} {
		t.Run(method, func(t *testing.T) {
			e, rec, _ := workspaceEnv(t)
			author := e.start(rec, "author", nil)
			other := e.start(rec, "other", nil)
			prfixture.FakeGH(t)
			e.mgr.Executable = ""
			as := func(id string) context.Context {
				return authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: id, Room: rec.ID})
			}
			got, err := e.svc.PRs.TrackPRRef(as(author.ID), rec.ID, author.ID, "7", "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			original := got.PR.GoalID
			target := other.Goal
			if original == "" || target == "" || original == target {
				t.Fatal("sessions must hold different goals")
			}
			call := func(ctx context.Context, session string) error {
				if method == "set" {
					return e.svc.PRs.SetPRGoal(ctx, rec.ID, got.PR.Key, target)
				}
				_, err := e.svc.PRs.TrackPRRef(ctx, rec.ID, session, "7", "", target, nil)
				return err
			}
			if err := call(as(other.ID), other.ID); err == nil {
				t.Fatal("an unrelated session changed the goal link")
			}
			p := e.svc.PRs.Watcher()
			checkLink := func() {
				r, err := p.Store.Get(got.PR.Key)
				if err != nil || r.GoalID != original {
					t.Fatalf("refused link changed the goal: %+v %v", r, err)
				}
			}
			checkLink()
			// Transfer after the record read but before its author rule finishes.
			ctx := &transferOnGoalActorRead{Context: as(author.ID), transferAt: 1}
			if method == "track" {
				ctx.transferAt = 2
			}
			transferred := false
			ctx.transfer = func() {
				st, err := p.Store.Read()
				if err != nil {
					t.Fatal(err)
				}
				st.Records[got.PR.Key].Author = other.ID
				prfixture.SaveState(t, p.Store.Root, st)
				transferred = true
			}
			if err := call(ctx, author.ID); !transferred || err == nil || !strings.Contains(err.Error(), "author changed") {
				t.Fatalf("transferred=%v, link error=%v; want stale-author refusal", transferred, err)
			}
			checkLink()
		})
	}
}

// Count only authenticated actor reads, not cancellation and GH context reads.
type transferOnGoalActorRead struct {
	context.Context
	reads, transferAt int
	transfer          func()
}

func (c *transferOnGoalActorRead) Value(key any) any {
	value := c.Context.Value(key)
	if _, ok := value.(authority.Actor); ok {
		c.reads++
		if c.reads == c.transferAt {
			c.transfer()
		}
	}
	return value
}
