package subagent

import (
	"dyna-agent/internal/room"
	"testing"
)

func TestQueueCancellationAndClaimBoundary(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	e.wait(info.ID, 1)
	store := e.m.Store
	rooms := e.m.RoomStore()
	post := func(from, text string) room.Message {
		t.Helper()
		m, err := rooms.Post(info.RoomID, room.PostInput{From: from, Human: from == info.Room, To: []string{info.Address()}, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	cancelled := post(info.Room, "cancel this")
	queue, err := store.SessionQueue(rooms, info.ID)
	if err != nil || len(queue) != 1 || !queue[0].Cancellable || !queue[0].Human || queue[0].State != "waiting" {
		t.Fatalf("waiting: %+v %v", queue, err)
	}
	if err = store.DeletePendingSteer(rooms, info.ID, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.DeletePendingSteer(rooms, info.ID, cancelled.ID); err != nil {
		t.Fatal("cancel not idempotent", err)
	}
	other := post("other-agent", "not owner input")
	if err = store.DeletePendingSteer(rooms, info.ID, other.ID); err == nil {
		t.Fatal("cancelled another sender's message")
	}
	claimed := post(info.Room, "keep this")
	meta, _ := store.ReadMeta(info.ID)
	gate := store.newTurnGate(rooms, meta, 1, meta.RoomCursor)
	if err = gate.Activate(); err != nil {
		t.Fatal(err)
	}
	got, _, err := gate.Read(0, false)
	if err != nil || len(got) != 2 {
		t.Fatalf("gate: %+v %v", got, err)
	}
	for _, msg := range got {
		if msg.ID == cancelled.ID {
			t.Fatal("delivered cancelled post")
		}
	}
	if err = store.DeletePendingSteer(rooms, info.ID, claimed.ID); err == nil {
		t.Fatal("cancelled claimed message")
	}
	queue, err = store.SessionQueue(rooms, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queue[0].State != "cancelled" || queue[1].Human || !queue[2].Human || queue[2].State != "delivering" || queue[2].Cancellable {
		t.Fatalf("claimed queue: %+v", queue)
	}
	if _, err = store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Steering = append(turn.Steering, Steer{ID: claimed.ID, Delivered: true}) }); err != nil {
		t.Fatal(err)
	}
	queue, err = store.SessionQueue(rooms, info.ID)
	if err != nil || queue[2].State != "delivered" {
		t.Fatalf("delivered: %+v %v", queue, err)
	}
	legacy := post(info.Room, "legacy dispatch")
	if _, err = store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Steering = append(turn.Steering, Steer{ID: legacy.ID}) }); err != nil {
		t.Fatal(err)
	}
	queue, err = store.SessionQueue(rooms, info.ID)
	if err != nil || queue[len(queue)-1].State != "delivering" || queue[len(queue)-1].Cancellable {
		t.Fatalf("legacy dispatch: %+v %v", queue, err)
	}
	drained, _, _ := e.m.drainRoomMessages(meta, Turn{N: 2})
	for _, msg := range drained {
		if msg.ID == cancelled.ID {
			t.Fatal("cancelled post drained into next turn")
		}
	}
}

func TestQueueCancellationRacesGateClaim(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	e.wait(info.ID, 1)
	store := e.m.Store
	rooms := e.m.RoomStore()
	meta, _ := store.ReadMeta(info.ID)
	gate := store.newTurnGate(rooms, meta, 1, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		msg, err := rooms.Post(info.RoomID, room.PostInput{From: info.Room, To: []string{info.Address()}, Text: "race input"})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		deleted := make(chan error, 1)
		go func() { <-start; deleted <- store.DeletePendingSteer(rooms, info.ID, msg.ID) }()
		close(start)
		got, _, err := gate.Read(0, false)
		if err != nil {
			t.Fatal(err)
		}
		cancelErr := <-deleted
		if cancelErr == nil && len(got) != 0 {
			t.Fatalf("cancelled post was claimed: %+v", got)
		}
		if cancelErr != nil && (len(got) != 1 || got[0].ID != msg.ID) {
			t.Fatalf("failed cancel lost delivery: %+v %v", got, cancelErr)
		}
	}
}
