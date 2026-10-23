package subagent

import (
	"testing"

	"dyna-agent/internal/room"
)

func TestConsolidationInboxGateIncrementalDelivery(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("RESPOND: target SLEEP:1")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: source SLEEP:1")
	b, _ = e.wait(b.ID, 1)
	c, _ := e.start("RESPOND: other source SLEEP:1")
	c, _ = e.wait(c.ID, 1)
	// Keep a completed historical turn separate from the latest receipt row.
	for _, info := range []Info{a, b, c} {
		if _, err := e.send(info.ID, "RESPOND: followup SLEEP:1", WakeOptions{}); err != nil {
			t.Fatal(err)
		}
		e.wait(info.ID, 2)
	}
	store, rooms := e.m.Store, e.m.rooms()
	post := func(to ...string) room.Message {
		t.Helper()
		msg, err := rooms.Post(a.RoomID, room.PostInput{From: a.Room, To: to, Text: "inherited input"})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	pending := post(b.Address())
	// The source advanced beyond an unacknowledged claim before consolidation.
	claimPostsFor(t, e, b.ID, 2, pending.ID)
	_, cursor, err := rooms.ReadAll(a.RoomID, 0)
	if err != nil {
		t.Fatal(err)
	}
	shared := post(b.Address(), c.Address())
	cancelled := post(b.Address())
	sourceDone := post(b.Address())
	targetDone := post(b.Address())
	cancelPostFor(t, e, b.ID, shared.ID)
	cancelPostFor(t, e, b.ID, cancelled.ID)
	if _, err := store.UpdateTurn(b.ID, 1, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: sourceDone.ID, Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTurn(a.ID, 1, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: targetDone.ID, Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	meta, err := store.UpdateMeta(a.ID, func(meta *Meta) {
		meta.Turns = 3
		meta.InheritedInboxes = []InheritedInbox{
			{Session: b.ID, Address: b.Address(), Cursor: cursor, PendingIDs: []string{pending.ID}},
			{Session: c.ID, Address: c.Address(), Cursor: cursor},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeTurn(a.ID, Turn{N: 3, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	gate := store.newTurnGate(rooms, meta, 3, cursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	read := func(want ...string) {
		t.Helper()
		got, _, err := gate.Read(0, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %+v, want IDs %v", got, want)
		}
		for i := range got {
			if got[i].ID != want[i] {
				t.Fatalf("got %+v, want IDs %v", got, want)
			}
		}
	}
	// Shared posts and unacknowledged claims are delivered once.
	read(shared.ID, pending.ID)
	// Corrupt completed histories after the first read. Repeated polls must use
	// cached receipts instead of reopening or decoding these growing histories.
	db, err := store.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if _, err := db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=1", []byte(`{"n":"invalid history"}`), id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		read()
	}
	if gate.store.inputInbox(gate.meta.ID, gate.turn).cache.origins[b.ID].cursor <= cursor {
		t.Fatal("inherited cursor did not advance")
	}
	if len(gate.store.inputInbox(gate.meta.ID, gate.turn).cache.origins[b.ID].pending) != 0 {
		t.Fatal("cache retained an already dispatched claim")
	}
	delayed := post(b.Address())
	read(delayed.ID)
	read()
	// An empty poll must not even inspect the latest source receipt row.
	latest, _, err := storedTurn(t.Context(), db, b.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=2", []byte(`{"n":"invalid latest"}`), b.ID); err != nil {
		t.Fatal(err)
	}
	read()
	if _, err := db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=2", latest, b.ID); err != nil {
		t.Fatal(err)
	}
	late := post(b.Address(), c.Address(), a.Address())
	read(late.ID)
	read()
	// No acknowledgement was stored for pending or late. They must be retried
	// by a new gate after a restart, but never twice within the original gate.
	for _, id := range []string{a.ID, b.ID, c.ID} {
		// Restore historical records so a new gate can rebuild its receipt cache.
		turn := Turn{N: 1}
		if id == a.ID {
			turn.Steering = []Steer{{ID: targetDone.ID, Delivered: true}}
		}
		if id == b.ID {
			turn.Steering = []Steer{{ID: sourceDone.ID, Delivered: true}}
		}
		if err := store.writeTurn(id, turn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateTurn(a.ID, 2, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: shared.ID, Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	gate.Deactivate()
	meta, err = store.UpdateMeta(a.ID, func(meta *Meta) { meta.Turns = 4 })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeTurn(a.ID, Turn{N: 4, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	gate = store.newTurnGate(rooms, meta, 4, gate.cursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	read(delayed.ID, late.ID, pending.ID)
	read()
	// A final empty read seals the gate without replaying an inherited post.
	if got, _, err := gate.Read(0, true); err != nil || len(got) != 0 {
		t.Fatalf("seal: %+v %v", got, err)
	}
	if _, ok := store.closedAt(a.ID, 4); !ok {
		t.Fatal("gate did not seal")
	}
}

func TestInboxReceiptCacheRefreshesLatestTurn(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("RESPOND: target SLEEP:1")
	a, _ = e.wait(a.ID, 1)
	cache := &inboxCache{}
	got, err := cache.delivered(e.m.Store, a.Meta)
	if err != nil || got["receipt"] {
		t.Fatalf("initial receipts: %v %v", got, err)
	}
	if _, err := e.m.Store.UpdateTurn(a.ID, 1, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: "receipt", Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	got, err = cache.delivered(e.m.Store, a.Meta)
	if err != nil || !got["receipt"] {
		t.Fatalf("new acknowledgement missed: %v %v", got, err)
	}
}
