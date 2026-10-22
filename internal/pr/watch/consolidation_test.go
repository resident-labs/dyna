package watch

import (
	"context"
	"dyna-agent/internal/pr/links"
	"strings"
	"testing"
)

func TestConsolidationMovesActiveRolesPreservesReviewHistoryAndAuthorNoise(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	oldAuthor := r.Author
	target := "ses_20261017-000003-cccccccc"
	oldReviewer := "ses_20261017-000004-dddddddd"
	if err := e.s.Store.update(func(st *State) error {
		cfg := st.Reviewers[r.RoomID]
		cfg.ReviewPublishing = PublishingLocal
		st.Reviewers[r.RoomID] = cfg
		p := st.Records[r.Key]
		p.Reviewer = oldReviewer
		p.RoundHead = p.Head
		p.Round = 2
		p.ReviewPublishing = PublishingLocal
		notice(p, oldAuthor, "pending CI notice", "before-consolidation")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := links.Consolidate(e.s.Store.Root, r.RoomID, target, map[string]bool{oldAuthor: true, oldReviewer: true}); err != nil {
		t.Fatal(err)
	}
	after, err := e.s.Store.Get(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	if after.Author != target || after.Reviewer != "" || len(after.ReviewHistory) != 1 || len(after.FormerAuthors) != 1 || after.FormerAuthors[0] != oldAuthor || after.Notices[0].Target != target || !strings.Contains(after.PauseReason, "independent reviewer") {
		t.Fatalf("role migration lost evidence: %+v", after)
	}
	if after.LookupDir != r.LookupDir || after.Dir != r.Dir || after.Head != r.Head {
		t.Fatal("worktree/head continuity changed")
	}
	events := []Event{{Session: oldAuthor, Text: "previous author progress " + Marker(oldAuthor), Commenter: "owner"}, {Session: oldReviewer, Text: "independent reviewer finding " + Marker(oldReviewer), Commenter: "owner"}, {Session: target, Text: "new author progress " + Marker(target), Commenter: "owner"}, {Text: "Please fix this", Commenter: "owner"}}
	decisions := (Rules{}).Triage(PRContext{AuthorSession: after.Author, FormerAuthors: after.FormerAuthors, OwnerLogin: "owner"}, events)
	for i, want := range []string{"drop", "keep", "drop", "keep"} {
		if decisions[i].Action != want {
			t.Fatalf("marker %d: %+v", i, decisions[i])
		}
	}
	// Replaying commit cannot add another review-history entry or author alias.
	if err = links.Consolidate(e.s.Store.Root, r.RoomID, target, map[string]bool{oldAuthor: true, oldReviewer: true}); err != nil {
		t.Fatal(err)
	}
	after, _ = e.s.Store.Get(r.Key)
	if len(after.ReviewHistory) != 1 || len(after.FormerAuthors) != 1 {
		t.Fatal("migration was not idempotent")
	}
	e.setCI("success", 1)
	e.tick()
	after, _ = e.s.Store.Get(r.Key)
	if after.Reviewer == "" || after.Reviewer == after.Author || after.Verdict != "" {
		t.Fatalf("self review not paused: %+v", after)
	}
}

func TestConsolidationKeepsCompletedHeadBoundVerdictAndRejectsSelfReview(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	reviewer := "ses_20261017-000004-dddddddd"
	target := "ses_20261017-000003-cccccccc"
	if err := e.s.Store.update(func(st *State) error {
		p := st.Records[r.Key]
		p.Reviewer = reviewer
		p.RoundHead = p.Head
		p.ReviewPublishing = PublishingLocal
		p.Verdict = "approve"
		p.Summary = "Full independent Markdown review."
		p.ReviewID = 12345
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := links.Consolidate(e.s.Store.Root, r.RoomID, target, map[string]bool{r.Author: true, reviewer: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := e.s.Store.Get(r.Key)
	if after.Reviewer != reviewer || after.RoundHead != r.Head || after.ReviewID != 12345 || after.Summary != "Full independent Markdown review." || after.Verdict != "approve" {
		t.Fatalf("historical verdict changed: %+v", after)
	}
	e.setCI("success", 1)
	e.write()
	if err := e.s.Store.update(func(st *State) error { p := st.Records[r.Key]; p.Reviewer = target; p.Verdict = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verdict(context.Background(), r.Key, target, r.Head, "approve", "Self approval must fail", 0); err == nil || !strings.Contains(err.Error(), "author") {
		t.Fatalf("self verdict accepted: %v", err)
	}
}
