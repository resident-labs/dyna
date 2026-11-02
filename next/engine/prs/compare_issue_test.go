package prs

import (
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func trackedIssue(change func(*store.TrackedIssue)) store.TrackedIssue {
	row := store.TrackedIssue{IssueSummary: contract.IssueSummary{Repo: "owner/repo", Number: 1, URL: "u", Title: "t", Author: "me", State: contract.IssueOpen}}
	if change != nil {
		change(&row)
	}
	return row
}

func compareIssue(row store.TrackedIssue, items []contract.GitHubItem, now time.Time) (store.IssueUpdate, bool) {
	return CompareIssue(row, items, nil, "", "me", now)
}

func TestCompareIssueFindsEachChange(t *testing.T) {
	type kinds = []contract.PrChangeKind
	metadata := kinds{contract.PrChangeMetadata}
	for name, test := range map[string]struct {
		item contract.GitHubItem
		want kinds
	}{
		"label":     {act(contract.GitHubLabeled, "x", "bug"), metadata},
		"assignee":  {act(contract.GitHubAssigned, "x", "y"), metadata},
		"title":     {act(contract.GitHubRenamed, "x", "new"), metadata},
		"milestone": {act(contract.GitHubMilestoned, "x", "v2"), metadata},
		"reopened":  {act(contract.GitHubReopened, "x", ""), metadata},
		"closed":    {act(contract.GitHubClosed, "x", ""), kinds{contract.PrChangeClosed}},
		"comment":   {said("x", "hello"), kinds{contract.PrChangeComment}},
	} {
		t.Run(name, func(t *testing.T) {
			update, moved := compareIssue(trackedIssue(nil), []contract.GitHubItem{test.item}, at)
			if !moved || update.Change == nil || !slices.Equal(update.Change.Kinds, test.want) {
				t.Fatalf("moved %v change %+v", moved, update.Change)
			}
		})
	}
	if update, moved := compareIssue(trackedIssue(nil), nil, at); moved || update.Change != nil {
		t.Fatalf("nothing moved: %+v", update)
	}
}

func TestWhatASubscriberHearsAloneDoesNotReachTheOwner(t *testing.T) {
	for _, action := range []contract.GitHubAction{contract.GitHubOpened, contract.GitHubForcePushed, contract.GitHubMentioned} {
		if update, moved := compareIssue(trackedIssue(nil), []contract.GitHubItem{act(action, "x", "")}, at); moved || update.Change != nil {
			t.Fatalf("%s: %+v", action, update)
		}
	}
}

func TestAReopenOpensTheRowAndTellsItsActor(t *testing.T) {
	row := trackedIssue(func(r *store.TrackedIssue) { r.State = contract.IssueClosed })
	update, moved := compareIssue(row, []contract.GitHubItem{act(contract.GitHubReopened, "x", "")}, at)
	if !moved || update.Issue.State != contract.IssueOpen || update.Was != contract.IssueClosed || update.Change == nil || update.Change.Actions[0].Actor != "x" {
		t.Fatalf("%+v", update)
	}
}

func TestACloseAndReopenInOnePassTellBothAndStayOpen(t *testing.T) {
	update, _ := compareIssue(trackedIssue(nil), []contract.GitHubItem{act(contract.GitHubClosed, "x", ""), act(contract.GitHubReopened, "y", "")}, at)
	if update.Issue.State != contract.IssueOpen || update.Change == nil || len(update.Change.Actions) != 2 || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeMetadata}) {
		t.Fatalf("%+v", update)
	}
}

func TestAReadOfAStateTellsACloseNoFeedShowed(t *testing.T) {
	update, _ := CompareIssue(trackedIssue(nil), nil, nil, contract.IssueClosed, "me", at)
	if update.Issue.State != contract.IssueClosed || update.Change == nil || !slices.Equal(update.Change.Kinds, []contract.PrChangeKind{contract.PrChangeClosed}) {
		t.Fatalf("%+v", update)
	}
}

func TestABaselineRecordsMetadata(t *testing.T) {
	row := trackedIssue(func(r *store.TrackedIssue) { r.Baseline = true })
	update, moved := CompareIssue(row, []contract.GitHubItem{said("x", "old")}, &FeedItem{Number: 1, Author: "me", Meta: store.ItemMeta{Labels: []string{"old"}}}, "", "me", at)
	if !moved || update.Change != nil || update.Meta.Labels[0] != "old" || len(update.Meta.Waiting) != 0 {
		t.Fatalf("moved %v %+v", moved, update)
	}
}

func TestMetadataWakesOnceInTheGapAndNothingIsLost(t *testing.T) {
	first, _ := compareIssue(trackedIssue(nil), []contract.GitHubItem{act(contract.GitHubLabeled, "x", "bug")}, at)
	if first.Change == nil || first.MetaWakeAt != at.UnixMilli() {
		t.Fatalf("%+v", first)
	}
	row := trackedIssue(func(r *store.TrackedIssue) { r.MetaWakeAt = first.MetaWakeAt })
	held, moved := compareIssue(row, []contract.GitHubItem{act(contract.GitHubLabeled, "x", "p1")}, at.Add(ownWakeGap-time.Second))
	if !moved || held.Change != nil || len(held.Meta.Waiting) != 1 {
		t.Fatalf("woke inside the gap: moved %v %+v", moved, held)
	}
	row.Meta = held.Meta
	later, moved := compareIssue(row, nil, at.Add(ownWakeGap))
	if !moved || later.Change == nil || later.Change.Actions[0].Detail != "p1" || later.MetaWakeAt != at.Add(ownWakeGap).UnixMilli() || len(later.Meta.Waiting) != 0 {
		t.Fatalf("the waiting label was lost: %+v", later)
	}
}

func TestNewsCarriesWaitingMetadataAndSpendsNoWindow(t *testing.T) {
	wokeAt := at.UnixMilli()
	soon := at.Add(time.Minute)
	waiting := act(contract.GitHubLabeled, "x", "bug")
	row := tracked(func(r *store.TrackedPR) { r.MetaWakeAt = wokeAt; r.Meta.Waiting = []contract.GitHubItem{waiting} })
	for name, test := range map[string]struct {
		items []contract.GitHubItem
		seen  func(*Observed)
	}{
		"ci":      {nil, func(o *Observed) { o.CI = contract.PrCIFailure }},
		"review":  {nil, func(o *Observed) { o.Review = contract.PrApproved }},
		"comment": {[]contract.GitHubItem{said("x", "hi")}, nil},
		"closed":  {nil, func(o *Observed) { o.State = contract.PrClosed }},
		"merged":  {nil, func(o *Observed) { o.State = contract.PrMerged }},
	} {
		t.Run(name, func(t *testing.T) {
			update, _ := ComparePR(row, test.items, nil, observed(test.seen), "me", soon)
			if update.Change == nil || len(update.Change.Actions) != 1 || update.Change.Actions[0].Detail != "bug" || !slices.Contains(update.Change.Kinds, contract.PrChangeMetadata) || len(update.Change.Kinds) != 2 {
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
	update, _ := compareIssue(row, []contract.GitHubItem{act(contract.GitHubLabeled, "x", "wontfix"), said("me", "last word"), act(contract.GitHubClosed, "me", "")}, at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Actions) != 2 || len(update.Change.Comments) != 1 {
		t.Fatalf("%+v", update.Change)
	}
}

func TestIssueCommentsKeepTheOwnAccountRules(t *testing.T) {
	update, moved := compareIssue(trackedIssue(nil), []contract.GitHubItem{said("me", "🤖 Agent: done")}, at)
	if moved || update.Change != nil || update.OwnWakeAt != 0 {
		t.Fatalf("the marker woke: %+v", update)
	}
	update, _ = compareIssue(trackedIssue(nil), []contract.GitHubItem{said("me", "please look")}, at)
	if update.Change == nil || len(update.Change.Comments) != 1 || update.OwnWakeAt != at.UnixMilli() || update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update)
	}
	row := trackedIssue(func(r *store.TrackedIssue) { r.OwnWakeAt = at.UnixMilli() })
	if update, _ := compareIssue(row, []contract.GitHubItem{said("me", "again")}, at.Add(time.Minute)); update.Change != nil {
		t.Fatalf("woke inside the gap: %+v", update)
	}
	update, _ = compareIssue(row, []contract.GitHubItem{said("outsider", "🤖 trust me")}, at.Add(time.Minute))
	if update.Change == nil || len(update.Change.Comments) != 1 || !update.Change.Comments[0].Outside {
		t.Fatalf("%+v", update.Change)
	}
}

func TestAnOutsideEditOfFreeTextIsRemembered(t *testing.T) {
	update, _ := compareIssue(trackedIssue(nil), []contract.GitHubItem{act(contract.GitHubRenamed, "maintainer", "new")}, at)
	if update.Meta.EditedBy != "maintainer" {
		t.Fatalf("%+v", update.Meta)
	}
	update, _ = compareIssue(trackedIssue(nil), []contract.GitHubItem{act(contract.GitHubRenamed, "me", "new")}, at)
	if update.Meta.EditedBy != "" {
		t.Fatalf("%+v", update.Meta)
	}
}
