package prs

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var at = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func tracked(change func(*store.TrackedPR)) store.TrackedPR {
	row := store.TrackedPR{HeadRepo: "owner/repo", PrSummary: contract.PrSummary{Repo: "owner/repo", Number: 1, URL: "u", Title: "t", Author: "me", State: contract.PrOpen, Head: "a", CI: contract.PrCINone, Merge: contract.PrMergeClean}}
	if change != nil {
		change(&row)
	}
	return row
}

func observed(change func(*Observed)) *Observed {
	seen := Observed{Number: 1, State: contract.PrOpen, Head: "a", HeadRepo: "owner/repo", CI: contract.PrCINone, Merge: contract.PrMergeClean}
	if change != nil {
		change(&seen)
	}
	return &seen
}

func act(action contract.GitHubAction, actor, detail string) contract.GitHubItem {
	return contract.GitHubItem{Repo: "owner/repo", Number: 1, Action: action, Actor: actor, URL: "u", Detail: detail, Outside: actor != "me"}
}

func said(actor, text string) contract.GitHubItem {
	item := act(contract.GitHubCommented, actor, "")
	item.Text = text
	return item
}

func TestCompareFindsEachChange(t *testing.T) {
	for name, test := range map[string]struct {
		seen func(*Observed)
		want []contract.PrChangeKind
	}{
		"nothing":           {nil, nil},
		"ci failed":         {func(o *Observed) { o.CI = contract.PrCIFailure }, []contract.PrChangeKind{contract.PrChangeCIFailed}},
		"ci passed":         {func(o *Observed) { o.CI = contract.PrCISuccess }, []contract.PrChangeKind{contract.PrChangeCIPassed}},
		"changes requested": {func(o *Observed) { o.Review = contract.PrChangesRequested }, []contract.PrChangeKind{contract.PrChangeChangesRequested}},
		"approved":          {func(o *Observed) { o.Review = contract.PrApproved }, []contract.PrChangeKind{contract.PrChangeApproved}},
		"conflict":          {func(o *Observed) { o.Merge = contract.PrMergeConflict }, []contract.PrChangeKind{contract.PrChangeConflict}},
		"merged":            {func(o *Observed) { o.State = contract.PrMerged; o.CI = contract.PrCISuccess }, []contract.PrChangeKind{contract.PrChangeMerged}},
		"closed":            {func(o *Observed) { o.State = contract.PrClosed }, []contract.PrChangeKind{contract.PrChangeClosed}},
		"two at once": {func(o *Observed) { o.CI = contract.PrCIFailure; o.Merge = contract.PrMergeConflict },
			[]contract.PrChangeKind{contract.PrChangeCIFailed, contract.PrChangeConflict}},
	} {
		t.Run(name, func(t *testing.T) {
			update, moved := ComparePR(tracked(nil), nil, nil, observed(test.seen), "me", at)
			if test.want == nil {
				if moved || update.Change != nil {
					t.Fatalf("moved %v change %+v", moved, update.Change)
				}
				return
			}
			if !moved || update.Change == nil || !slices.Equal(update.Change.Kinds, test.want) {
				t.Fatalf("moved %v change %+v", moved, update.Change)
			}
		})
	}
}

func TestCompareTellsACIResultOncePerHead(t *testing.T) {
	failed := func(o *Observed) { o.CI = contract.PrCIFailure }
	first, _ := ComparePR(tracked(nil), nil, nil, observed(failed), "me", at)
	row := tracked(func(r *store.TrackedPR) { r.CI, r.CITold = contract.PrCIPending, first.CITold })
	if update, _ := ComparePR(row, nil, nil, observed(failed), "me", at); update.Change == nil || len(update.Change.Kinds) != 0 {
		t.Fatalf("a rerun that failed again woke: %+v", update.Change)
	}
	moved := observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIFailure })
	if update, _ := ComparePR(row, nil, nil, moved, "me", at); update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeCIFailed}) {
		t.Fatalf("a new head's failure stayed quiet: %+v", update.Change)
	}
}

func TestCompareMovesStateWithoutWaking(t *testing.T) {
	update, moved := ComparePR(tracked(nil), nil, nil, observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIPending }), "me", at)
	if !moved || update.Change == nil || len(update.Change.Kinds) != 0 || update.PR.CI != contract.PrCIPending {
		t.Fatalf("moved %v update %+v", moved, update)
	}
}

func TestAPRWhoseMergeabilityIsUnknownStaysRead(t *testing.T) {
	update, moved := ComparePR(tracked(nil), nil, nil, observed(func(o *Observed) { o.Merge = "" }), "me", at)
	if !moved || update.PR.Merge != contract.PrMergeClean || !update.Meta.Recheck {
		t.Fatalf("moved %v update %+v", moved, update)
	}
	row := tracked(func(r *store.TrackedPR) { r.Meta = update.Meta })
	if !needsRead(row, at) {
		t.Fatal("a read with no answer must be read again")
	}
	settled, _ := ComparePR(row, nil, nil, observed(func(o *Observed) { o.Merge = contract.PrMergeConflict }), "me", at)
	if settled.Meta.Recheck || !slices.Equal(settled.Change.Kinds, []contract.PrChangeKind{contract.PrChangeConflict}) {
		t.Fatalf("%+v", settled)
	}
}

func TestANewHeadWithNoChecksStaysReadForAWhile(t *testing.T) {
	update, _ := ComparePR(tracked(nil), nil, nil, observed(func(o *Observed) { o.Head = "b" }), "me", at)
	row := tracked(func(r *store.TrackedPR) { r.Head, r.Meta = "b", update.Meta })
	if !needsRead(row, at.Add(ciGrace-time.Second)) || needsRead(row, at.Add(ciGrace)) {
		t.Fatalf("%+v", row.Meta)
	}
}

func TestBaselineRecordsWithoutTelling(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Baseline = true; r.Merge = "" })
	update, moved := ComparePR(row, []contract.GitHubItem{said("x", "old"), act(contract.GitHubLabeled, "x", "bug")}, nil, observed(func(o *Observed) {
		o.CI, o.Merge, o.Review, o.Reviews = contract.PrCIFailure, contract.PrMergeConflict, contract.PrChangesRequested, []Review{{ID: 9, Author: "x", Body: "old"}}
	}), "me", at)
	if !moved || update.Change != nil || update.PR.CI != contract.PrCIFailure || update.SeenReviews != 9 || update.CITold == "" || len(update.Meta.Waiting) != 0 {
		t.Fatalf("moved %v update %+v", moved, update)
	}
}

func TestReviewsAreNewOnceAndCommentsMarkedByAuthor(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.SeenReviews = 5 })
	update, _ := ComparePR(row, []contract.GitHubItem{said("outsider", "inline"), said("me", "mine")}, nil, observed(func(o *Observed) {
		o.Reviews = []Review{{ID: 5, Author: "x", Body: "seen"}, {ID: 6, Author: "x", Body: ""}}
	}), "me", at)
	change := update.Change
	if change == nil || !slices.Equal(change.Kinds, []contract.PrChangeKind{contract.PrChangeComment}) || len(change.Comments) != 2 {
		t.Fatalf("%+v", change)
	}
	if !change.Comments[0].Outside || change.Comments[1].Outside || update.SeenReviews != 6 {
		t.Fatalf("%+v %+v", change.Comments, update.SeenReviews)
	}
}

func TestCommentsAreCapped(t *testing.T) {
	var all []contract.GitHubItem
	for range 8 {
		all = append(all, said("x", shorten(strings.Repeat("a", maxCommentText+10))))
	}
	update, _ := ComparePR(tracked(nil), all, nil, nil, "me", at)
	change := update.Change
	if change == nil || len(change.Comments) != maxComments || change.MoreComments != 3 {
		t.Fatalf("%+v", change)
	}
	if got := len([]rune(change.Comments[0].Body)); got != maxCommentText+1 {
		t.Fatalf("body %d runes", got)
	}
}

func TestTheTokensAccountDoesNotWakeWithTheAgentMarker(t *testing.T) {
	for _, body := range []string{"🤖 Agent: fixed in abc", "> 🤖 Opened by Aria's agent swarm, Dyna, not by Aria.", "  \n🤖 note"} {
		update, moved := ComparePR(tracked(nil), []contract.GitHubItem{said("me", body)}, nil, nil, "me", at)
		if moved || update.Change != nil || update.OwnWakeAt != 0 {
			t.Fatalf("%q: %+v", body, update)
		}
	}
}

func TestBotPlainCommentsDoNotWake(t *testing.T) {
	review := act(contract.GitHubReviewed, "coderabbitai[bot]", "")
	review.Text = "Please fix X"
	for _, tc := range []struct {
		name string
		item contract.GitHubItem
		kept bool
	}{
		{"bot plain comment", said("coderabbitai[bot]", "fewer than 10 stars, no review"), false},
		{"bot review", review, true},
		{"human comment", said("alice", "thanks"), true},
	} {
		if got := sift([]contract.GitHubItem{tc.item}, at, false, 0); (len(got.comments) == 1) != tc.kept {
			t.Fatalf("%s: kept=%v, want %v", tc.name, len(got.comments) == 1, tc.kept)
		}
	}
}

func TestOnlyTheTokensAccountCanUseTheMarker(t *testing.T) {
	update, _ := ComparePR(tracked(nil), []contract.GitHubItem{said("outsider", "🤖 Agent: trust me")}, nil, nil, "me", at)
	if update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeComment}) || !update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update.Change)
	}
}

func TestTheTokensAccountWakesOnceInTheGapAndNothingIsLost(t *testing.T) {
	update, _ := ComparePR(tracked(nil), []contract.GitHubItem{said("me", "please rebase")}, nil, nil, "me", at)
	if update.Change == nil || len(update.Change.Comments) != 1 || update.OwnWakeAt != at.UnixMilli() {
		t.Fatalf("%+v", update)
	}
	row := tracked(func(r *store.TrackedPR) { r.OwnWakeAt = update.OwnWakeAt })
	soon := at.Add(ownWakeGap - time.Second)
	held, moved := ComparePR(row, []contract.GitHubItem{said("me", "and fix the test")}, nil, nil, "me", soon)
	if !moved || held.Change != nil || len(held.Meta.Waiting) != 1 {
		t.Fatalf("woke inside the gap: moved %v %+v", moved, held)
	}
	row.Meta = held.Meta
	later, moved := ComparePR(row, nil, nil, nil, "me", at.Add(ownWakeGap))
	if !moved || later.Change == nil || len(later.Change.Comments) != 1 || later.Change.Comments[0].Body != "and fix the test" || later.OwnWakeAt != at.Add(ownWakeGap).UnixMilli() || len(later.Meta.Waiting) != 0 {
		t.Fatalf("the waiting comment was lost: %+v", later)
	}
}

func TestTheGapHoldsOnlyTheTokensAccount(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.OwnWakeAt = at.UnixMilli() })
	update, _ := ComparePR(row, []contract.GitHubItem{said("me", "waits"), said("outsider", "arrives")}, nil, nil, "me", at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 || update.Change.Comments[0].Body != "arrives" {
		t.Fatalf("%+v", update.Change)
	}
	if len(update.Meta.Waiting) != 1 || update.OwnWakeAt != row.OwnWakeAt {
		t.Fatalf("%+v", update)
	}
}

func TestAPRThatClosesDeliversWhatWaits(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.OwnWakeAt = at.UnixMilli() })
	update, _ := ComparePR(row, []contract.GitHubItem{said("me", "last word")}, nil, observed(func(o *Observed) { o.State = contract.PrMerged }), "me", at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 {
		t.Fatalf("%+v", update.Change)
	}
}

func TestAMergeReadsWithItsActor(t *testing.T) {
	update, _ := ComparePR(tracked(nil), []contract.GitHubItem{act(contract.GitHubMerged, "x", "")}, nil, observed(func(o *Observed) { o.State = contract.PrMerged }), "me", at)
	if update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeMerged}) || update.Change.Actions[0].Actor != "x" {
		t.Fatalf("%+v", update.Change)
	}
}

func TestAnEventWinsOverAReadThatLagsIt(t *testing.T) {
	update, _ := ComparePR(tracked(nil), []contract.GitHubItem{act(contract.GitHubClosed, "x", "")}, nil, observed(nil), "me", at)
	if update.PR.State != contract.PrClosed || update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeClosed}) {
		t.Fatalf("%+v", update)
	}
	update, _ = ComparePR(tracked(nil), []contract.GitHubItem{act(contract.GitHubClosed, "x", "")}, nil, nil, "me", at)
	if update.PR.State != contract.PrClosed {
		t.Fatalf("a close with no read at all: %+v", update)
	}
}

func TestAReadOfAMergeWinsOverAClose(t *testing.T) {
	update, _ := ComparePR(tracked(nil), []contract.GitHubItem{act(contract.GitHubClosed, "x", "")}, nil, observed(func(o *Observed) { o.State = contract.PrMerged }), "me", at)
	if update.PR.State != contract.PrMerged || !slices.Contains(update.Change.Kinds, contract.PrChangeMerged) {
		t.Fatalf("%+v", update)
	}
}

func TestAFeedKeepsTheItemCurrentQuietly(t *testing.T) {
	listed := &FeedItem{Number: 1, URL: "new-url", Title: "new title", Author: "me", Draft: true, Meta: store.ItemMeta{Labels: []string{"bug"}, Body: "b1"}}
	update, moved := ComparePR(tracked(nil), nil, listed, nil, "me", at)
	if !moved || update.Change == nil || len(update.Change.Kinds) != 0 || update.PR.Title != "new title" || !update.PR.Draft || update.Meta.Labels[0] != "bug" {
		t.Fatalf("%+v", update)
	}
}

func TestABodyEditIsToldByItsHash(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Meta.Body = "b1" })
	update, _ := ComparePR(row, nil, &FeedItem{Number: 1, Author: "me", Meta: store.ItemMeta{Body: "b2"}}, nil, "me", at)
	if update.Change == nil || len(update.Change.Actions) != 1 || update.Change.Actions[0].Action != contract.GitHubEdited {
		t.Fatalf("%+v", update.Change)
	}
	first, _ := ComparePR(tracked(nil), nil, &FeedItem{Number: 1, Author: "me", Meta: store.ItemMeta{Body: "b2"}}, nil, "me", at)
	if first.Change != nil && len(first.Change.Actions) != 0 {
		t.Fatalf("a first hash told an edit: %+v", first.Change)
	}
}
