package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/goals"
)

// Folding b into a merges b's checklist into a's effective goal: b's goal
// closes as folded, items move with fresh IDs, assignees follow to a, and
// session records remap. No goal row changes hands.
func TestConsolidationMergesGoalItems(t *testing.T) {
	e, _ := consolidationEnv(t)
	e.joinRunners()
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	stampParent(t, b)
	c, _ := e.start("RESPOND: c")
	c, _ = e.wait(c.ID, 1)
	e.joinRunners()()
	gs := e.m.goalStore()
	gA, gB := a.Goal, b.Goal
	// b is also assignee of an item on a shared goal: only the assignee
	// follows b to a.
	shared, err := gs.Add(b.RoomID, "owner", goals.Input{Text: "Shared", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gs.AddItem(b.RoomID, shared.ID, "owner", "shared work", b.ID); err != nil {
		t.Fatal(err)
	}
	// The survivor also works on two other goals. Its spawn goal still
	// determines where absorbed checklist items go.
	for _, text := range []string{"Other goal one", "Other goal two"} {
		other, err := gs.Add(a.RoomID, "owner", goals.Input{Text: text, Shared: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := gs.AddItem(a.RoomID, other.ID, "owner", text, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	fold := consolidatePair(t, e, a, b)
	submitAll(t, e, fold)
	finishConsolidation(t, e, fold)

	fresh, err := gs.Get(b.RoomID, gB)
	if err != nil || fresh.State != "done" || fresh.Done == nil || !strings.Contains(fresh.Done.Summary, "folded") {
		t.Fatalf("gB = %+v err=%v", fresh, err)
	}
	items, err := gs.Items(b.RoomID, gA)
	if err != nil || len(items) != 1 {
		t.Fatalf("gA items = %v err=%v", items, err)
	}
	if items[0].Assignee != c.ID || items[0].State != goals.ItemOpen {
		t.Fatalf("moved item = %+v", items[0])
	}
	got, err := e.m.Store.Info(c.ID)
	if err != nil || got.Goal != gA || got.Item != items[0].ID {
		t.Fatalf("c -> goal %q item %q, want %q %q (err %v)", got.Goal, got.Item, gA, items[0].ID, err)
	}
	sharedItems, err := gs.Items(b.RoomID, shared.ID)
	if err != nil || len(sharedItems) != 1 || sharedItems[0].Assignee != a.ID {
		t.Fatalf("shared item = %v err=%v", sharedItems, err)
	}
	// a keeps exactly its own goal; nothing was transferred to it.
	held, err := gs.OpenHeldBy(b.RoomID, a.ID)
	if err != nil || len(held) != 1 || held[0].ID != gA {
		t.Fatalf("a holds = %v err=%v", held, err)
	}
}

// A fold with no valid target goal must fail before any source is archived.
func TestConsolidationValidatesGoalBeforeClosingSources(t *testing.T) {
	e, _ := consolidationEnv(t)
	e.joinRunners()
	a, _ := e.start("RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	e.joinRunners()()
	gs := e.m.goalStore()
	if _, err := gs.Finish(a.RoomID, a.Goal, a.ID, "finished", false); err != nil {
		t.Fatal(err)
	}
	// The spawn goal is closed and cannot receive the source checklist.
	if _, err := gs.AddItem(b.RoomID, b.Goal, b.ID, "Keep this work", ""); err != nil {
		t.Fatal(err)
	}
	fold := consolidatePair(t, e, a, b)
	if err := e.m.commitConsolidation(fold); err == nil {
		t.Fatal("fold into a closed goal succeeded")
	}
	fresh, err := e.m.Store.Info(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Closed.IsZero() || fresh.ConsumedBy != "" {
		t.Fatalf("invalid fold archived source: %+v", fresh.Meta)
	}
	items, err := gs.Items(b.RoomID, b.Goal)
	if err != nil || len(items) != 1 {
		t.Fatalf("source checklist changed: %v, %v", items, err)
	}
}
