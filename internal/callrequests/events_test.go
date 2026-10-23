package callrequests

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/state"
)

func TestAskPublishesOnlyCommittedChanges(t *testing.T) {
	s := testStore(t)
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var events []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) {
		rows, err := s.List("room")
		if err != nil || len(rows) != 1 {
			t.Fatalf("publication before commit: %v %v", rows, err)
		}
		events = append(events, ev)
	})
	row, created, err := s.Ask("room", "Please join", "session")
	if err != nil || !created {
		t.Fatalf("Ask: %v %v", created, err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one committed change", len(events))
	}
	raw, err := json.Marshal(events[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Kind   string            `json:"kind"`
		Room   string            `json:"roomId"`
		Change listchange.Change `json:"change"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	var item Request
	if err := json.Unmarshal(wire.Change.Item, &item); err != nil {
		t.Fatal(err)
	}
	if events[0].Topic != "call-requests" || events[0].Room != "room" || wire.Kind != "call-requests" || wire.Room != "room" || wire.Change.Op != "upsert" || wire.Change.ID != row.ID || item != row {
		t.Fatalf("wrong event: %s", raw)
	}
	if _, created, err := s.Ask("room", "Please join", "session"); err != nil || created {
		t.Fatalf("repeat: %v %v", created, err)
	}
	if _, _, err := s.Ask("room", "", "session"); err == nil {
		t.Fatal("invalid request accepted")
	}
	if len(events) != 1 {
		t.Fatalf("no-op or failure published: %d", len(events))
	}
}
