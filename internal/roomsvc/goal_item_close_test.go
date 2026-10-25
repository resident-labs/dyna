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
	lastItemPosts := func() int {
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
		return posted
	}
	if posted := lastItemPosts(); posted != 1 {
		t.Fatalf("the holder got %d last-item posts, want 1", posted)
	}
	// Correcting a closed item to dropped, or setting it done again, describes
	// the same completion: neither posts again.
	for _, state := range []string{goals.ItemDropped, goals.ItemDone} {
		if _, err := e.svc.SetGoalItemState(as(child.ID), r.ID, g.ID, it.ID, state); err != nil {
			t.Fatal(err)
		}
		if posted := lastItemPosts(); posted != 1 {
			t.Fatalf("changing a closed item to %s posted %d last-item posts, want 1", state, posted)
		}
		if stored, err := store.Get(r.ID, g.ID); err != nil || stored.ItemsClosed != 1 {
			t.Fatalf("changing a closed item to %s counts %d closes: %+v, %v", state, stored.ItemsClosed, stored, err)
		}
	}
	// A reopened item that closes again is a second close of the goal's last
	// item, so the holder is told again instead of keeping the first post.
	if _, err := e.svc.SetGoalItemState(as(lead.ID), r.ID, g.ID, it.ID, goals.ItemOpen); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetGoalItemState(as(child.ID), r.ID, g.ID, it.ID, goals.ItemDone); err != nil {
		t.Fatal(err)
	}
	if posted := lastItemPosts(); posted != 2 {
		t.Fatalf("the second close posted %d last-item posts, want 2", posted)
	}
	if stored, err := store.Get(r.ID, g.ID); err != nil || !stored.Open() || stored.ItemsClosed != 2 {
		t.Fatalf("the goal left its items closed: %+v, %v", stored, err)
	}
	if _, err := e.svc.FinishGoal(as(child.ID), r.ID, g.ID, "Reviewed", false); err == nil {
		t.Fatal("a child ended the goal of its holder")
	}
	if g, err := e.svc.FinishGoal(as(lead.ID), r.ID, g.ID, "Shipped", false); err != nil || g.Open() {
		t.Fatalf("the holder could not end its goal: %+v, %v", g, err)
	}
}
