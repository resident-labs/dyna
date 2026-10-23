package goals

import (
	"strings"
	"testing"
	"time"
)

var statusNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func item(state, assignee string) Item {
	return Item{ID: "i1", Text: "work", State: state, Assignee: assignee}
}

func TestComputeStatusPrecedence(t *testing.T) {
	open := Goal{ID: "g1", State: "active", Holders: []string{"a"}, CreatedAt: statusNow.Add(-time.Hour)}
	for _, tc := range []struct {
		name   string
		goal   Goal
		items  []Item
		facts  Facts
		want   string
		reason string
	}{
		{"running beats waiting", open, []Item{item(ItemOpen, "a")}, Facts{Busy: true, OpenPR: true}, StatusOnTrack, "turn running"},
		{"failed last turn", open, []Item{item(ItemOpen, "a")}, Facts{LastFailed: true}, StatusErrored, "last turn failed"},
		{"no work no failure is on track", open, nil, Facts{}, StatusOnTrack, ""},
		{"closed checklist", open, []Item{item(ItemDone, "a"), item(ItemDropped, "")}, Facts{}, StatusReadyToClose, "all items closed"},
		{"merged pr with open items stays open", open, []Item{item(ItemOpen, "a")}, Facts{MergedPR: true}, StatusOnTrack, "merged PR"},
		{"merged pr with closed checklist", open, []Item{item(ItemDone, "a")}, Facts{MergedPR: true}, StatusReadyToClose, "merged PR"},
		{"goal without items merged pr", open, nil, Facts{MergedPR: true}, StatusReadyToClose, "merged PR"},
		{"unheld goal", Goal{ID: "g2", State: "active"}, nil, Facts{}, StatusUnowned, "no holder"},
		{"goal left with the owner", Goal{ID: "g10", State: "active", LegacyOwnerHeld: true}, nil, Facts{}, StatusNeedsRoot, "root asks the owner"},
		{"held goal with unassigned item is not unowned", open, []Item{item(ItemOpen, "")}, Facts{Busy: true}, StatusOnTrack, "1 unassigned"},
		{"pending pr", open, []Item{item(ItemOpen, "a")}, Facts{OpenPR: true}, StatusWaiting, "PR pending"},
		{"fresh creation", open, []Item{item(ItemOpen, "a")}, Facts{}, StatusOnTrack, ""},
		{"done goal", Goal{ID: "g7", State: "done"}, nil, Facts{LastFailed: true}, "done", "goal done"},
		{"done goal keeps counts", Goal{ID: "g9", State: "done"}, []Item{item(ItemDone, "a"), item(ItemOpen, "")}, Facts{}, "done", "goal done"},
		{"dropped goal", Goal{ID: "g8", State: "dropped"}, nil, Facts{}, "dropped", "goal dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeStatus(tc.goal, tc.items, tc.facts, statusNow)
			if got.Label != tc.want {
				t.Fatalf("label = %q (%s), want %q", got.Label, got.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", got.Reason, tc.reason)
			}
			if tc.name == "done goal keeps counts" && (got.Done != 1 || got.Open != 1) {
				t.Fatalf("closed goal counts = %+v", got)
			}
		})
	}
}

func TestComputeStatusCounts(t *testing.T) {
	g := Goal{ID: "g1", State: "active", Holders: []string{"a"}}
	items := []Item{item(ItemOpen, "a"), item(ItemOpen, ""), item(ItemDone, "a"), item(ItemDropped, "")}
	got := ComputeStatus(g, items, Facts{Busy: true}, statusNow)
	if got.Open != 2 || got.Done != 1 || got.Dropped != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if !strings.Contains(got.Reason, "1 unassigned") {
		t.Fatalf("reason = %q, want the unassigned count", got.Reason)
	}
}

func TestComputeItemStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		item   Item
		facts  Facts
		want   string
		reason string
	}{
		{"unassigned open item", item(ItemOpen, ""), Facts{}, StatusUnowned, "no assignee"},
		{"busy assignee", item(ItemOpen, "a"), Facts{Busy: true}, StatusOnTrack, "may be another item"},
		{"failed assignee", item(ItemOpen, "a"), Facts{LastFailed: true}, StatusErrored, "last turn failed"},
		{"waiting assignee", item(ItemOpen, "a"), Facts{OpenPR: true}, StatusWaiting, "PR pending"},
		{"done item", item(ItemDone, "a"), Facts{}, ItemDone, "done"},
		{"dropped item", item(ItemDropped, "a"), Facts{}, ItemDropped, "dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeItemStatus(tc.item, tc.facts, statusNow)
			if got.Label != tc.want {
				t.Fatalf("label = %q (%s), want %q", got.Label, got.Reason, tc.want)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", got.Reason, tc.reason)
			}
		})
	}
}
