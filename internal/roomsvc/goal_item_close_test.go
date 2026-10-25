package roomsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

// TestAChildsLastItemLeavesTheGoalOpen keeps a goal open when the child that
// holds its only item finishes that item: the holder learns the items are all
// closed, and only the holder ends the goal itself.
func TestAChildsLastItemLeavesTheGoalOpen(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	child := e.start(r, "child", &lead)
	as := func(id string) context.Context {
		return authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	}
	store := e.svc.goalService().Store
	g, err := store.Add(r.ID, "owner", goals.Input{Text: "Ship the relay", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	it, err := e.svc.AddGoalItem(as(lead.ID), r.ID, g.ID, "Review the relay", "@child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetGoalItemState(as(child.ID), r.ID, g.ID, it.ID, goals.ItemDone); err != nil {
		t.Fatal(err)
	}
	if stored, err := store.Get(r.ID, g.ID); err != nil || !stored.Open() {
		t.Fatalf("a child's finished item ended the goal: %+v, %v", stored, err)
	}
	messages, err := e.svc.rooms().Messages(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	posted := 0
	for _, msg := range messages {
		ev, ok := msg.Goal()
		if !ok || ev.Type != room.GoalItemsDone || ev.GoalID != g.ID {
			continue
		}
		posted++
		if len(msg.To) != 1 || msg.To[0] != lead.Address() {
			t.Fatalf("the last-item post did not reach the holder: %+v", msg)
		}
	}
	if posted != 1 {
		t.Fatalf("the holder got %d last-item posts, want 1", posted)
	}
	if _, err := e.svc.FinishGoal(as(child.ID), r.ID, g.ID, "Reviewed", false); err == nil {
		t.Fatal("a child ended the goal of its holder")
	}
	if g, err := e.svc.FinishGoal(as(lead.ID), r.ID, g.ID, "Shipped", false); err != nil || g.Open() {
		t.Fatalf("the holder could not end its goal: %+v, %v", g, err)
	}
}
