package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/goals"
)

// A lead's child is a delegation item on the lead's goal, not a goal of its
// own; a grandchild lands on the same goal.
func TestChildrenDelegateAsItems(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	gs := e.m.goalStore()
	stampParent(t, lead)
	child, _ := e.start("RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	if child.Goal != lead.Goal || child.Item == "" {
		t.Fatalf("child goal=%q item=%q, want an item on %q", child.Goal, child.Item, lead.Goal)
	}
	items, err := gs.Items(lead.RoomID, lead.Goal)
	if err != nil || len(items) != 1 || items[0].Assignee != child.ID {
		t.Fatalf("items=%v err=%v", items, err)
	}
	held, err := gs.OpenHeldBy(lead.RoomID, child.ID)
	if err != nil || len(held) != 0 {
		t.Fatalf("child holds goals: %v err=%v", held, err)
	}
	// The child is assigned item i1 on the lead's goal, so its own child
	// delegates on the same goal.
	stampParent(t, child)
	grand, _ := e.start("RESPOND: grand")
	grand, _ = e.wait(grand.ID, 1)
	if grand.Goal != lead.Goal || grand.Item == "" || grand.Item == child.Item {
		t.Fatalf("grandchild goal=%q item=%q", grand.Goal, grand.Item)
	}
	items, err = gs.Items(lead.RoomID, lead.Goal)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

// Finish refuses while an open item is assigned, and passes once the item is
// done.
func TestFinishBlocksOnOpenAssignedItem(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	child, _ := e.start("RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	e.joinRunners()()
	if _, err := e.m.Finish(e.ctx, child.ID); err == nil || !strings.Contains(err.Error(), "dyna goal item done") {
		t.Fatalf("finish with open item: %v", err)
	}
	if _, err := e.m.goalStore().SetItemState(child.RoomID, child.Goal, child.Item, child.ID, goals.ItemDone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Finish(e.ctx, child.ID); err != nil {
		t.Fatalf("finish after item done: %v", err)
	}
}

// Closing a session returns its unfinished items unassigned, with a returned
// event, and leaves the parent goal open.
func TestCloseReturnsOpenItems(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	child, _ := e.start("RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	e.joinRunners()()
	if _, err := e.m.Close(e.ctx, child.Address()); err != nil {
		t.Fatal(err)
	}
	gs := e.m.goalStore()
	items, err := gs.Items(lead.RoomID, lead.Goal)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if items[0].Assignee != "" || items[0].State != goals.ItemOpen {
		t.Fatalf("item not returned: %+v", items[0])
	}
	g, err := gs.Get(lead.RoomID, lead.Goal)
	if err != nil || !g.Open() {
		t.Fatalf("goal = %+v err=%v", g, err)
	}
	events, err := gs.Events(lead.RoomID, lead.Goal, 0)
	if err != nil || len(events) == 0 || events[0].Event != "returned" || events[0].Item != items[0].ID {
		t.Fatalf("events=%v err=%v", events, err)
	}
}
