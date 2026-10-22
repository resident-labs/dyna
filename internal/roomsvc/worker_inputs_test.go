package roomsvc

import (
	"context"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"path/filepath"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/watch"
)

func TestGoalTickProgressDoesNotWakeKeeper(t *testing.T) {
	e := newEnv(t)
	rec := e.room("keeper-inputs")
	svc := e.svc.goalService()
	goal, err := svc.Store.Add(rec.ID, "owner", goals.Input{Text: "Ship", Holders: []string{"owner"}})
	if err != nil {
		t.Fatal(err)
	}
	work, version := map[string]string{}, ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	goalRows := map[string]string{}
	e.svc.scanGoals(goalRows, work, &version, true, emit)
	if _, err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	// A check-in changes room keeper state, not goal rows.
	if len(events) != 0 {
		t.Fatalf("keeper woke itself: %+v", events)
	}
	// Keeper bookkeeping changes room state, not the goal definition.
	if err := svc.Store.Update(func(st *goals.State) error {
		st.Rooms[rec.ID].Keeper.LastError = "bookkeeping"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	if len(events) != 0 {
		t.Fatalf("keeper bookkeeping woke keeper: %+v", events)
	}
	if _, err := svc.Store.Edit(rec.ID, goal.ID, "owner", "Ship the engine", nil, false); err != nil {
		t.Fatal(err)
	}
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	if len(events) != 1 || !events[0].WorkerWake {
		t.Fatalf("goal edit did not wake keeper: %+v", events)
	}
}

func TestPRTickProgressDoesNotWakeWatcher(t *testing.T) {
	e := newEnv(t)
	rec := e.room("pr-inputs")
	key := "github.com/owner/repo#17"
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1,
		Records:   map[string]*watch.Record{key: {Key: key, Kind: "github", Host: "github.com", Repo: "owner/repo", Number: 17, RoomID: rec.ID, State: "open", Watching: true}},
		Reviewers: map[string]watch.Reviewer{rec.ID: {Disabled: true}},
	})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	e.svc.scanPRs(signatures, work, &version, true, emit)
	svc := e.svc.PRs.Watcher()
	// A failed executable lookup saves a poll error without starting a process.
	svc.GH.Executable = filepath.Join(t.TempDir(), "missing-gh")
	if _, err := svc.Tick(context.Background()); err == nil {
		t.Fatal("expected poll error")
	}
	r, err := svc.Store.Get(key)
	if err != nil || r.Error == "" {
		t.Fatalf("poll result was not saved: %+v %v", r, err)
	}
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) == 0 {
		t.Fatal("poll progress did not update the display")
	}
	wakes := 0
	for _, event := range events {
		if event.WorkerWake {
			wakes++
		}
	}
	if wakes != 1 {
		t.Fatalf("unverified head woke watcher %d times, want 1: %+v", wakes, events)
	}
	events = nil
	if err := svc.SetReviewerProfile(rec.ID, "mock"); err != nil {
		t.Fatal(err)
	}
	e.svc.scanPRs(signatures, work, &version, false, emit)
	wakes = 0
	for _, event := range events {
		if event.WorkerWake {
			wakes++
		}
	}
	if wakes != 1 {
		t.Fatalf("settings woke watcher %d times, want 1: %+v", wakes, events)
	}
}
