package roomsvc

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/goals"
)

func TestGoalExpediteExpiryEmitsWithoutAWrite(t *testing.T) {
	e := newEnv(t)
	rec := e.room("expedite-events")
	store := e.svc.goalService().Store
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	goal, err := store.Add(rec.ID, "owner", goals.Input{Text: "Ship", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, work, version := map[string]string{}, map[string]string{}, ""
	var events []Event
	scan := func(first bool) {
		e.svc.scanGoalsAt(now, rows, work, &version, first, func(ev Event) { events = append(events, ev) })
	}
	scan(true)
	if _, err := store.Expedite(rec.ID, goal.ID, false); err != nil {
		t.Fatal(err)
	}
	scan(false)
	if len(events) != 1 || events[0].Change == nil {
		t.Fatalf("expedite must publish the goal: %+v", events)
	}
	var changed goals.Goal
	if err := json.Unmarshal(events[0].Change.Item, &changed); err != nil || changed.ExpediteUntil == nil {
		t.Fatalf("expedite event has no priority: %+v %v", changed, err)
	}
	events = nil
	before, _ := store.Revision()
	now = now.Add(goals.ExpediteTTL - time.Nanosecond)
	scan(false)
	if len(events) != 0 {
		t.Fatal("priority expired before its deadline")
	}
	now = now.Add(time.Nanosecond)
	scan(false)
	scan(false)
	if len(events) != 1 || events[0].Kind != "goals" || events[0].RoomID != rec.ID || events[0].Change == nil || events[0].Change.ID != goal.ID {
		t.Fatalf("expiry must publish one goal update: %+v", events)
	}
	var expired goals.Goal
	if err := json.Unmarshal(events[0].Change.Item, &expired); err != nil || expired.ExpediteUntil != nil {
		t.Fatalf("expiry event kept priority: %+v %v", expired, err)
	}
	if after, _ := store.Revision(); after != before {
		t.Fatal("expiry test unexpectedly depended on a state write")
	}
}
