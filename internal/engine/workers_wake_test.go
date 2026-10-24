package engine

import (
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/store/storetest"
)

// The room's own address names its root seat, so a wake follows the seat's
// stored row and never punctuation in an address (D-39): a binding that names
// the seat address reaches no session, and a session address never wakes the
// seat by its shape.
func TestSeatWakeFollowsStoredRowsOnly(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "seat-wake"})
	if err != nil {
		t.Fatal(err)
	}
	signals := newWorkerSignals(rooms)
	other := "ses_20261021-000001-aaaaaaaa"
	seatPost, err := rooms.Post(rec.ID, room.PostInput{Wake: true, NeedsRoot: true, From: rec.Name + ":child",
		To: []string{rec.Name}, SessionIDs: map[string]string{rec.Name: other}, Text: "Root must choose."})
	if err != nil {
		t.Fatal(err)
	}
	signals.wake(roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{{Message: seatPost}}})
	batch := signals.takeSessions()
	if !batch.roots[rec.ID] || batch.targets[other] {
		t.Fatalf("seat wake = roots %v, targets %v; want the room's root and no session", batch.roots, batch.targets)
	}
	// A session's own post wakes that session and never the seat.
	sessionPost, err := rooms.Post(rec.ID, room.PostInput{Wake: true, From: rec.Name + ":child",
		To: []string{rec.Name + ":peer"}, SessionIDs: map[string]string{rec.Name + ":peer": other}, Text: "Peer, take a look."})
	if err != nil {
		t.Fatal(err)
	}
	signals.wake(roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{{Message: sessionPost}}})
	batch = signals.takeSessions()
	if !batch.targets[other] || batch.roots[rec.ID] {
		t.Fatalf("session wake = roots %v, targets %v; want the session alone", batch.roots, batch.targets)
	}
}
