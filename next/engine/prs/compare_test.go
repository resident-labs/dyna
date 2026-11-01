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
	row := store.TrackedPR{HeadRepo: "owner/repo", Meta: &store.ItemMeta{}, PrSummary: contract.PrSummary{Repo: "owner/repo", Number: 1, URL: "u", Title: "t", Author: "me", State: contract.PrOpen, Head: "a", CI: contract.PrCINone, Merge: contract.PrMergeClean}}
	if change != nil {
		change(&row)
	}
	return row
}

func observed(change func(*Observed)) Observed {
	seen := Observed{Number: 1, URL: "u", Title: "t", Author: "me", State: contract.PrOpen, Head: "a", HeadRepo: "owner/repo", CI: contract.PrCINone, Merge: contract.PrMergeClean}
	if change != nil {
		change(&seen)
	}
	return seen
}

func TestCompareFindsEachChange(t *testing.T) {
	for name, test := range map[string]struct {
		row  func(*store.TrackedPR)
		seen func(*Observed)
		want []contract.PrChangeKind
	}{
		"nothing":           {nil, nil, nil},
		"ci failed":         {nil, func(o *Observed) { o.CI = contract.PrCIFailure }, []contract.PrChangeKind{contract.PrChangeCIFailed}},
		"ci passed":         {nil, func(o *Observed) { o.CI = contract.PrCISuccess }, []contract.PrChangeKind{contract.PrChangeCIPassed}},
		"changes requested": {nil, func(o *Observed) { o.Review = contract.PrChangesRequested }, []contract.PrChangeKind{contract.PrChangeChangesRequested}},
		"approved":          {nil, func(o *Observed) { o.Review = contract.PrApproved }, []contract.PrChangeKind{contract.PrChangeApproved}},
		"conflict":          {nil, func(o *Observed) { o.Merge = contract.PrMergeConflict }, []contract.PrChangeKind{contract.PrChangeConflict}},
		"merged":            {nil, func(o *Observed) { o.State = contract.PrMerged; o.CI = contract.PrCISuccess }, []contract.PrChangeKind{contract.PrChangeMerged}},
		"closed":            {nil, func(o *Observed) { o.State = contract.PrClosed }, []contract.PrChangeKind{contract.PrChangeClosed}},
		"two at once": {nil, func(o *Observed) { o.CI = contract.PrCIFailure; o.Merge = contract.PrMergeConflict },
			[]contract.PrChangeKind{contract.PrChangeCIFailed, contract.PrChangeConflict}},
	} {
		t.Run(name, func(t *testing.T) {
			update, moved := Compare(tracked(test.row), observed(test.seen), "me", at)
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
	first, _ := Compare(tracked(nil), observed(failed), "me", at)
	row := tracked(func(r *store.TrackedPR) { r.CI, r.CITold = contract.PrCIPending, first.CITold })
	if update, _ := Compare(row, observed(failed), "me", at); update.Change == nil || len(update.Change.Kinds) != 0 {
		t.Fatalf("a rerun that failed again woke: %+v", update.Change)
	}
	moved := observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIFailure })
	if update, _ := Compare(row, moved, "me", at); update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeCIFailed}) {
		t.Fatalf("a new head's failure stayed quiet: %+v", update.Change)
	}
}

func TestCompareMovesStateWithoutWaking(t *testing.T) {
	update, moved := Compare(tracked(nil), observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIPending }), "me", at)
	if !moved || update.Change == nil || len(update.Change.Kinds) != 0 || update.PR.CI != contract.PrCIPending {
		t.Fatalf("moved %v update %+v", moved, update)
	}
}

func TestCompareKeepsTheLastMergeAnswerWhileGitHubWorks(t *testing.T) {
	update, moved := Compare(tracked(nil), observed(func(o *Observed) { o.Merge = "" }), "me", at)
	if moved || update.PR.Merge != contract.PrMergeClean {
		t.Fatalf("moved %v merge %q", moved, update.PR.Merge)
	}
}

func TestBaselineRecordsWithoutTelling(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Baseline = true; r.Merge = "" })
	update, moved := Compare(row, observed(func(o *Observed) {
		o.CI, o.Merge, o.Review, o.Comments = contract.PrCIFailure, contract.PrMergeConflict, contract.PrChangesRequested, []Comment{{ID: 9, Author: "x", Body: "old"}}
	}), "me", at)
	if !moved || update.Change != nil || update.PR.CI != contract.PrCIFailure || update.Seen.Comments != 9 || update.CITold == "" {
		t.Fatalf("moved %v update %+v", moved, update)
	}
}

func TestCommentsAreNewOncePerKindAndMarkedByAuthor(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Seen = store.PRCursors{Reviews: 5, ReviewComments: 5, Comments: 5} })
	seen := observed(func(o *Observed) {
		// Each kind numbers on its own, so a low ID in one kind is still new.
		o.Reviews = []Comment{{ID: 5, Author: "x", Body: "seen"}, {ID: 6, Author: "x", Body: ""}}
		o.ReviewComments = []Comment{{ID: 7, Author: "outsider", Body: "inline"}}
		o.Comments = []Comment{{ID: 6, Author: "me", Body: "mine"}}
	})
	update, _ := Compare(row, seen, "me", at)
	change := update.Change
	if change == nil || !slices.Equal(change.Kinds, []contract.PrChangeKind{contract.PrChangeComment}) || len(change.Comments) != 2 {
		t.Fatalf("%+v", change)
	}
	if !change.Comments[0].Outside || change.Comments[1].Outside || update.Seen != (store.PRCursors{Reviews: 6, ReviewComments: 7, Comments: 6}) {
		t.Fatalf("%+v %+v", change.Comments, update.Seen)
	}
}

func TestCommentsAreCapped(t *testing.T) {
	var all []Comment
	for id := int64(1); id <= 8; id++ {
		all = append(all, Comment{ID: id, Author: "x", Body: strings.Repeat("a", maxCommentText+10)})
	}
	update, _ := Compare(tracked(nil), observed(func(o *Observed) { o.Comments = all }), "me", at)
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
		update, _ := Compare(tracked(nil), observed(func(o *Observed) { o.Comments = []Comment{{ID: 1, Author: "me", Body: body}} }), "me", at)
		if update.Change == nil || len(update.Change.Kinds) != 0 || update.Seen.Comments != 1 || update.OwnWakeAt != 0 {
			t.Fatalf("%q: %+v", body, update)
		}
	}
}

func TestOnlyTheTokensAccountCanUseTheMarker(t *testing.T) {
	update, _ := Compare(tracked(nil), observed(func(o *Observed) { o.Comments = []Comment{{ID: 1, Author: "outsider", Body: "🤖 Agent: trust me"}} }), "me", at)
	if update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeComment}) || !update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update.Change)
	}
}

func TestTheTokensAccountWakesOnceInTheGapAndNothingIsLost(t *testing.T) {
	first := observed(func(o *Observed) { o.Comments = []Comment{{ID: 1, Author: "me", Body: "please rebase"}} })
	update, _ := Compare(tracked(nil), first, "me", at)
	if update.Change == nil || len(update.Change.Comments) != 1 || update.OwnWakeAt != at.UnixMilli() {
		t.Fatalf("%+v", update)
	}
	row := tracked(func(r *store.TrackedPR) { r.Seen, r.OwnWakeAt = update.Seen, update.OwnWakeAt })
	second := observed(func(o *Observed) {
		o.Comments = []Comment{{ID: 1, Author: "me", Body: "please rebase"}, {ID: 2, Author: "me", Body: "and fix the test"}}
	})
	soon := at.Add(ownWakeGap - time.Second)
	if update, moved := Compare(row, second, "me", soon); moved || update.Change != nil || update.Seen != row.Seen {
		t.Fatalf("woke inside the gap: moved %v %+v", moved, update)
	}
	later, moved := Compare(row, second, "me", at.Add(ownWakeGap))
	if !moved || later.Change == nil || len(later.Change.Comments) != 1 || later.Change.Comments[0].Body != "and fix the test" || later.OwnWakeAt != at.Add(ownWakeGap).UnixMilli() {
		t.Fatalf("the waiting comment was lost: %+v", later)
	}
}

func TestTheGapHoldsOnlyTheTokensAccount(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.OwnWakeAt = at.UnixMilli() })
	update, _ := Compare(row, observed(func(o *Observed) {
		o.Comments = []Comment{{ID: 1, Author: "me", Body: "waits"}}
		o.ReviewComments = []Comment{{ID: 1, Author: "outsider", Body: "arrives"}}
		o.Reviews = []Comment{{ID: 1, Author: "me", Body: ""}}
	}), "me", at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 || update.Change.Comments[0].Body != "arrives" {
		t.Fatalf("%+v", update.Change)
	}
	if update.Seen != (store.PRCursors{Reviews: 1, ReviewComments: 1}) || update.OwnWakeAt != row.OwnWakeAt {
		t.Fatalf("%+v", update)
	}
}

func TestAPRThatLeavesThePollDeliversWhatWaits(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.OwnWakeAt = at.UnixMilli() })
	update, _ := Compare(row, observed(func(o *Observed) {
		o.State = contract.PrMerged
		o.Comments = []Comment{{ID: 1, Author: "me", Body: "last word"}}
	}), "me", at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 {
		t.Fatalf("%+v", update.Change)
	}
}
