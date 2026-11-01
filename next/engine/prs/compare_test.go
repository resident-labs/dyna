package prs

import (
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func tracked(change func(*store.TrackedPR)) store.TrackedPR {
	row := store.TrackedPR{HeadRepo: "owner/repo", PrSummary: contract.PrSummary{Repo: "owner/repo", Number: 1, URL: "u", Title: "t", Author: "me", State: contract.PrOpen, Head: "a", CI: contract.PrCINone, Merge: contract.PrMergeClean}}
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
			update, moved := Compare(tracked(test.row), observed(test.seen), "me")
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
	first, _ := Compare(tracked(nil), observed(failed), "me")
	row := tracked(func(r *store.TrackedPR) { r.CI, r.CITold = contract.PrCIPending, first.CITold })
	if update, _ := Compare(row, observed(failed), "me"); update.Change == nil || len(update.Change.Kinds) != 0 {
		t.Fatalf("a rerun that failed again woke: %+v", update.Change)
	}
	moved := observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIFailure })
	if update, _ := Compare(row, moved, "me"); update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeCIFailed}) {
		t.Fatalf("a new head's failure stayed quiet: %+v", update.Change)
	}
}

func TestCompareMovesStateWithoutWaking(t *testing.T) {
	update, moved := Compare(tracked(nil), observed(func(o *Observed) { o.Head, o.CI = "b", contract.PrCIPending }), "me")
	if !moved || update.Change == nil || len(update.Change.Kinds) != 0 || update.PR.CI != contract.PrCIPending {
		t.Fatalf("moved %v update %+v", moved, update)
	}
}

func TestCompareKeepsTheLastMergeAnswerWhileGitHubWorks(t *testing.T) {
	update, moved := Compare(tracked(nil), observed(func(o *Observed) { o.Merge = "" }), "me")
	if moved || update.PR.Merge != contract.PrMergeClean {
		t.Fatalf("moved %v merge %q", moved, update.PR.Merge)
	}
}

func TestBaselineRecordsWithoutTelling(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Baseline = true; r.Merge = "" })
	update, moved := Compare(row, observed(func(o *Observed) {
		o.CI, o.Merge, o.Review, o.Comments = contract.PrCIFailure, contract.PrMergeConflict, contract.PrChangesRequested, []Comment{{ID: 9, Author: "x", Body: "old"}}
	}), "me")
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
	update, _ := Compare(row, seen, "me")
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
	update, _ := Compare(tracked(nil), observed(func(o *Observed) { o.Comments = all }), "me")
	change := update.Change
	if change == nil || len(change.Comments) != maxComments || change.MoreComments != 3 {
		t.Fatalf("%+v", change)
	}
	if got := len([]rune(change.Comments[0].Body)); got != maxCommentText+1 {
		t.Fatalf("body %d runes", got)
	}
}
