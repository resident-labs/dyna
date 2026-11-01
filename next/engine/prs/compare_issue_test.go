package prs

import (
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func trackedIssue(change func(*store.TrackedIssue)) store.TrackedIssue {
	row := store.TrackedIssue{Meta: &store.ItemMeta{Title: "t"}, IssueSummary: contract.IssueSummary{Repo: "owner/repo", Number: 1, URL: "u", Title: "t", Author: "me", State: contract.IssueOpen}}
	if change != nil {
		change(&row)
	}
	return row
}

func observedIssue(change func(*ObservedIssue)) ObservedIssue {
	seen := ObservedIssue{Number: 1, URL: "u", Author: "me", State: contract.IssueOpen, Meta: store.ItemMeta{Title: "t"}}
	if change != nil {
		change(&seen)
	}
	return seen
}

func TestCompareIssueFindsEachChange(t *testing.T) {
	type kinds = []contract.PrChangeKind
	metadata := kinds{contract.PrChangeMetadata}
	for name, test := range map[string]struct {
		seen func(*ObservedIssue)
		want kinds
		meta func(*contract.MetaChange) bool
	}{
		"nothing":   {nil, nil, nil},
		"label":     {func(o *ObservedIssue) { o.Meta.Labels = []string{"bug"} }, metadata, func(m *contract.MetaChange) bool { return slices.Equal(m.LabelsAdded, []string{"bug"}) }},
		"assignee":  {func(o *ObservedIssue) { o.Meta.Assignees = []string{"x"} }, metadata, func(m *contract.MetaChange) bool { return slices.Equal(m.Assigned, []string{"x"}) }},
		"title":     {func(o *ObservedIssue) { o.Meta.Title = "new" }, metadata, func(m *contract.MetaChange) bool { return m.Title == "new" }},
		"milestone": {func(o *ObservedIssue) { o.Meta.Milestone = "v2" }, metadata, func(m *contract.MetaChange) bool { return m.Milestone == "v2" && !m.MilestoneRemoved }},
		"body":      {func(o *ObservedIssue) { o.Meta.EditedAt = "2026-11-01T12:00:00Z" }, metadata, func(m *contract.MetaChange) bool { return m.BodyEdited }},
		"reopened":  {func(o *ObservedIssue) { o.Meta.Reopened = 1 }, metadata, func(m *contract.MetaChange) bool { return m.Reopened }},
		"closed":    {func(o *ObservedIssue) { o.State = contract.IssueClosed }, kinds{contract.PrChangeClosed}, nil},
		"comment":   {func(o *ObservedIssue) { o.Comments = []Comment{{ID: 1, Author: "x", Body: "hello"}} }, kinds{contract.PrChangeComment}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			update, moved := CompareIssue(trackedIssue(nil), observedIssue(test.seen), "me", at)
			if test.want == nil {
				if moved || update.Change != nil {
					t.Fatalf("moved %v change %+v", moved, update.Change)
				}
				return
			}
			if !moved || update.Change == nil || !slices.Equal(update.Change.Kinds, test.want) {
				t.Fatalf("moved %v change %+v", moved, update.Change)
			}
			if test.meta != nil && (update.Change.Meta == nil || !test.meta(update.Change.Meta)) {
				t.Fatalf("meta %+v", update.Change.Meta)
			}
		})
	}
}

func TestDiffMetaSeesRemovalsAndIgnoresNothing(t *testing.T) {
	told := store.ItemMeta{Title: "t", Labels: []string{"a", "b"}, Assignees: []string{"x"}, Milestone: "v1", Reviewers: []string{"r"}, Draft: true}
	if change := diffMeta(told, told); change != nil {
		t.Fatalf("no change gave %+v", change)
	}
	seen := store.ItemMeta{Title: "t", Labels: []string{"b", "c"}}
	change := diffMeta(told, seen)
	if change == nil || !slices.Equal(change.LabelsAdded, []string{"c"}) || !slices.Equal(change.LabelsRemoved, []string{"a"}) || !slices.Equal(change.Unassigned, []string{"x"}) ||
		!change.MilestoneRemoved || change.Milestone != "" || !slices.Equal(change.ReviewersRemoved, []string{"r"}) || !change.Ready || change.Draft {
		t.Fatalf("%+v", change)
	}
	if change := diffMeta(seen, told); change == nil || !change.Draft || change.Ready || !slices.Equal(change.ReviewersAdded, []string{"r"}) {
		t.Fatalf("%+v", change)
	}
}

func TestFirstMetadataReadRecordsWithoutTellingOrWaking(t *testing.T) {
	row := tracked(func(r *store.TrackedPR) { r.Meta = nil })
	update, moved := Compare(row, observed(func(o *Observed) { o.Meta.Labels = []string{"old"} }), "me", at)
	if !moved || update.Change != nil || update.Meta == nil || update.Meta.Labels[0] != "old" || update.MetaWakeAt != 0 {
		t.Fatalf("moved %v %+v", moved, update)
	}
}

func TestABaselineRecordsMetadata(t *testing.T) {
	row := trackedIssue(func(r *store.TrackedIssue) { r.Baseline, r.Meta = true, nil })
	update, moved := CompareIssue(row, observedIssue(func(o *ObservedIssue) {
		o.Meta.Labels, o.Comments = []string{"old"}, []Comment{{ID: 9, Author: "x", Body: "old"}}
	}), "me", at)
	if !moved || update.Change != nil || update.Meta == nil || update.SeenComments != 9 {
		t.Fatalf("moved %v %+v", moved, update)
	}
}

func TestMetadataWakesOnceInTheGapAndNothingIsLost(t *testing.T) {
	bug := func(o *ObservedIssue) { o.Meta.Labels = []string{"bug"} }
	first, _ := CompareIssue(trackedIssue(nil), observedIssue(bug), "me", at)
	if first.Change == nil || first.MetaWakeAt != at.UnixMilli() {
		t.Fatalf("%+v", first)
	}
	row := trackedIssue(func(r *store.TrackedIssue) { r.Meta, r.MetaWakeAt = first.Meta, first.MetaWakeAt })
	both := func(o *ObservedIssue) { o.Meta.Labels = []string{"bug", "p1"} }
	if update, moved := CompareIssue(row, observedIssue(both), "me", at.Add(ownWakeGap-time.Second)); moved || update.Change != nil || update.Meta != row.Meta {
		t.Fatalf("woke inside the gap: moved %v %+v", moved, update)
	}
	later, moved := CompareIssue(row, observedIssue(both), "me", at.Add(ownWakeGap))
	if !moved || later.Change == nil || !slices.Equal(later.Change.Meta.LabelsAdded, []string{"p1"}) || later.MetaWakeAt != at.Add(ownWakeGap).UnixMilli() {
		t.Fatalf("the waiting label was lost: %+v", later)
	}
}

func TestNewsCarriesWaitingMetadataAndSpendsNoWindow(t *testing.T) {
	wokeAt := at.UnixMilli()
	soon := at.Add(time.Minute)
	row := tracked(func(r *store.TrackedPR) { r.MetaWakeAt = wokeAt })
	for name, seen := range map[string]func(*Observed){
		"ci":      func(o *Observed) { o.CI = contract.PrCIFailure },
		"review":  func(o *Observed) { o.Review = contract.PrApproved },
		"comment": func(o *Observed) { o.Comments = []Comment{{ID: 1, Author: "x", Body: "hi"}} },
		"closed":  func(o *Observed) { o.State = contract.PrClosed },
		"merged":  func(o *Observed) { o.State = contract.PrMerged },
	} {
		t.Run(name, func(t *testing.T) {
			update, _ := Compare(row, observed(func(o *Observed) { o.Meta.Labels = []string{"bug"}; seen(o) }), "me", soon)
			if update.Change == nil || update.Change.Meta == nil || update.Change.Meta.LabelsAdded[0] != "bug" || !slices.Contains(update.Change.Kinds, contract.PrChangeMetadata) || len(update.Change.Kinds) != 2 {
				t.Fatalf("%+v", update.Change)
			}
			if update.MetaWakeAt != wokeAt {
				t.Fatalf("spent the window: %d", update.MetaWakeAt)
			}
		})
	}
}

func TestAnIssueThatClosesDeliversWhatWaits(t *testing.T) {
	row := trackedIssue(func(r *store.TrackedIssue) { r.OwnWakeAt, r.MetaWakeAt = at.UnixMilli(), at.UnixMilli() })
	update, _ := CompareIssue(row, observedIssue(func(o *ObservedIssue) {
		o.State, o.Meta.Labels = contract.IssueClosed, []string{"wontfix"}
		o.Comments = []Comment{{ID: 1, Author: "me", Body: "last word"}}
	}), "me", at.Add(time.Minute))
	if update.Change == nil || update.Change.Meta == nil || len(update.Change.Comments) != 1 {
		t.Fatalf("%+v", update.Change)
	}
}

func TestIssueCommentsKeepTheOwnAccountRules(t *testing.T) {
	update, _ := CompareIssue(trackedIssue(nil), observedIssue(func(o *ObservedIssue) { o.Comments = []Comment{{ID: 1, Author: "me", Body: "🤖 Agent: done"}} }), "me", at)
	if update.Change == nil || len(update.Change.Kinds) != 0 || update.SeenComments != 1 || update.OwnWakeAt != 0 {
		t.Fatalf("the marker woke: %+v", update)
	}
	update, _ = CompareIssue(trackedIssue(nil), observedIssue(func(o *ObservedIssue) { o.Comments = []Comment{{ID: 1, Author: "me", Body: "please look"}} }), "me", at)
	if update.Change == nil || len(update.Change.Comments) != 1 || update.OwnWakeAt != at.UnixMilli() || update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update)
	}
	row := trackedIssue(func(r *store.TrackedIssue) { r.OwnWakeAt = at.UnixMilli() })
	if update, moved := CompareIssue(row, observedIssue(func(o *ObservedIssue) { o.Comments = []Comment{{ID: 1, Author: "me", Body: "again"}} }), "me", at.Add(time.Minute)); moved || update.Change != nil {
		t.Fatalf("woke inside the gap: %+v", update)
	}
	update, _ = CompareIssue(row, observedIssue(func(o *ObservedIssue) { o.Comments = []Comment{{ID: 1, Author: "outsider", Body: "🤖 trust me"}} }), "me", at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 || !update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update.Change)
	}
}
