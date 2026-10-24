package parentcheckin

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestPendingCheckInSurvivesRestartAndIntervalChanges(t *testing.T) {
	root := t.TempDir()
	if err := Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	targets := []Target{{ID: "parent", Room: "room"}}
	busy := true
	var ids []string
	makeScheduler := func() *Scheduler {
		return &Scheduler{Root: root, Now: func() time.Time { return now }, Interval: func() (time.Duration, error) { return interval, nil }, Targets: func() ([]Target, error) { return targets, nil }, Deliver: func(_ context.Context, _ Target, id string) (bool, error) { ids = append(ids, id); return !busy, nil }}
	}
	tick := func(s *Scheduler) time.Time {
		t.Helper()
		next, err := s.Tick(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	s := makeScheduler()
	if next := tick(s); !next.Equal(now.Add(interval)) {
		t.Fatalf("initial deadline %v", next)
	}
	now = now.Add(interval)
	if next := tick(s); !next.IsZero() || len(ids) != 1 {
		t.Fatalf("busy parent deadline %v, attempts %v", next, ids)
	}
	// Reconstruct the service and reopen its database, as an engine restart does.
	if err := database.Close(root); err != nil {
		t.Fatal(err)
	}
	s = makeScheduler()
	now = now.Add(time.Hour)
	busy = false
	if next := tick(s); !next.Equal(now.Add(interval)) || len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("restart lost one pending check-in: %v %v", next, ids)
	}
	tick(s)
	if len(ids) != 2 {
		t.Fatal("check-in stacked before next interval")
	}
	interval = 2 * time.Minute
	if next := tick(s); !next.Equal(now.Add(interval)) {
		t.Fatalf("interval edit kept old deadline %v", next)
	}
	now = now.Add(interval)
	tick(s)
	if len(ids) != 3 {
		t.Fatal("edited interval did not deliver")
	}
	interval = 0
	tick(s)
	now = now.Add(time.Hour)
	tick(s)
	if len(ids) != 3 {
		t.Fatal("disabled check-in delivered")
	}
	interval = 2 * time.Minute
	tick(s)
	targets = nil
	now = now.Add(interval)
	tick(s)
	if len(ids) != 3 {
		t.Fatal("ineligible parent received check-in")
	}
}

func TestEligibilityUsesOnlyActiveDirectChildren(t *testing.T) {
	for _, role := range []string{"root", ""} {
		live := []subagent.Info{
			{Meta: subagent.Meta{ID: "parent", RoomID: "r", Role: role}, Status: subagent.StatusDormant},
			{Meta: subagent.Meta{ID: "child", RoomID: "r", ParentSession: "parent"}, Status: subagent.StatusIdle},
			{Meta: subagent.Meta{ID: "grandchild", RoomID: "r", ParentSession: "child"}, Status: subagent.StatusRunning},
			{Meta: subagent.Meta{ID: "dormant", RoomID: "r", ParentSession: "parent"}, Status: subagent.StatusDormant},
			{Meta: subagent.Meta{ID: "closed", RoomID: "r", ParentSession: "parent"}, Status: subagent.StatusClosed},
			{Meta: subagent.Meta{ID: "other-room", RoomID: "other", ParentSession: "parent"}, Status: subagent.StatusQueued},
		}
		got := children(live)
		if len(got["parent"]) != 1 || got["parent"][0].ID != "child" {
			t.Fatalf("role %q: parent children %+v", role, got)
		}
		live[1].Status = subagent.StatusDormant
		if len(children(live)["parent"]) != 0 {
			t.Fatalf("role %q woke for dormant child or grandchild", role)
		}
	}
}

func TestCheckinEventIsStalestFirst(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	parent := subagent.Info{Meta: subagent.Meta{ID: "parent"}}
	got := checkinEvent(parent, []childReport{
		{name: "new", state: room.CheckinQueued, work: "g2/i1: Check parser", prs: "no open PRs", since: now.Add(-10 * time.Minute)},
		{name: "old", state: room.CheckinIdle, work: "g1: Fix parser", prs: "open PR #42: open", since: now.Add(-12 * time.Minute)},
	}, now)
	if got.Parent != "parent" || got.WindowStart != now.UnixMilli() {
		t.Fatalf("check-in identity = %+v", got)
	}
	if got.Counts != (room.CheckinCounts{Queued: 1, Idle: 1}) {
		t.Fatalf("counts = %+v", got.Counts)
	}
	want := []room.CheckinChild{
		{Name: "old", State: room.CheckinIdle, Age: "12m", Goal: "g1: Fix parser", PR: "open PR #42: open"},
		{Name: "new", State: room.CheckinQueued, Age: "10m", Goal: "g2/i1: Check parser", PR: "no open PRs"},
	}
	if len(got.Children) != len(want) {
		t.Fatalf("children = %+v", got.Children)
	}
	for i := range want {
		if got.Children[i] != want[i] {
			t.Fatalf("child %d = %+v, want %+v", i, got.Children[i], want[i])
		}
	}
	wantDetail := "old · idle for 12m · g1: Fix parser · open PR #42: open\nnew · queued for 10m · g2/i1: Check parser · no open PRs"
	if got.Detail != wantDetail {
		t.Fatalf("detail:\n%s\nwant:\n%s", got.Detail, wantDetail)
	}
}
