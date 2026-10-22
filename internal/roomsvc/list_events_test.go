package roomsvc

import (
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/goals"
)

func TestOldGoalUpdateCarriesItemOutsideTheHead(t *testing.T) {
	e := newEnv(t)
	rec := e.room("old-goal-event")
	store := e.svc.goalService().Store
	store.Now = func() time.Time { return time.Now().Add(-48 * time.Hour) }
	goal, err := store.Add(rec.ID, "owner", goals.Input{Text: "Old goal", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(rec.ID, goal.ID, "owner", "Done", false); err != nil {
		t.Fatal(err)
	}
	snapshots, version := map[string]string{}, ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	goalRows := map[string]string{}
	e.svc.scanGoals(goalRows, snapshots, &version, true, emit)
	if err := store.Update(func(st *goals.State) error {
		st.Rooms[rec.ID].Goals[goal.ID].Text = "Updated closed history"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.svc.scanGoals(goalRows, snapshots, &version, false, emit)
	if len(events) != 1 {
		t.Fatalf("goal events: %+v", events)
	}
	data, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Kind   string `json:"kind"`
		RoomID string `json:"roomId"`
		Change *struct {
			Op     string     `json:"op"`
			ID     string     `json:"id"`
			InHead *bool      `json:"inHead"`
			Item   goals.Goal `json:"item"`
		} `json:"change"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Change == nil {
		t.Fatalf("goal event has no changed item: %s", data)
	}
	if wire.Kind != "goals" || wire.RoomID != rec.ID || wire.Change.Op != "upsert" || wire.Change.ID != goal.ID || wire.Change.Item.Text != "Updated closed history" || wire.Change.InHead == nil || *wire.Change.InHead {
		t.Fatalf("old history event: %s", data)
	}
	page, err := e.svc.Goals(rec.ID, "", 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("old update entered fresh head: %+v %v", page, err)
	}
}

func TestPRAndChangeEventsKeepIndependentDefaultHeads(t *testing.T) {
	e := newEnv(t)
	rec := e.room("independent-heads")
	now := time.Now()
	st := watch.State{Version: 1, Records: map[string]*watch.Record{"pr": {Key: "pr", Kind: "github", RoomID: rec.ID, State: "closed", ClosedAt: now.Add(-time.Hour), Title: "Before"}}}
	for i := range 50 {
		key := fmt.Sprintf("local-%02d", i)
		st.Records[key] = &watch.Record{Key: key, Kind: "local", RoomID: rec.ID, State: "closed", ClosedAt: now.Add(-time.Minute)}
	}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.scanPRs(signatures, work, &version, true, func(Event) { t.Fatal("initial PR event") })
	st.Records["pr"].Title = "After"
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	var events []Event
	e.svc.scanPRs(signatures, work, &version, false, func(event Event) { events = append(events, event) })
	if len(events) != 2 {
		t.Fatalf("PR update emitted %d list events, want 2", len(events))
	}
	heads := map[string]bool{}
	for _, event := range events {
		if event.Change == nil || event.Change.Op != "upsert" || event.Change.ID != "pr" || event.Change.InHead == nil {
			t.Fatalf("PR event lost its item: %+v", event)
		}
		heads[event.Kind] = *event.Change.InHead
	}
	if !heads["prs"] || heads["changes"] {
		t.Fatalf("PR and Changes default heads differ: %v", heads)
	}
}
