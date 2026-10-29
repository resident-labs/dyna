package state

import (
	"sync"
	"testing"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

// A committed PR state write publishes one prstate event through the state
// write path, so the engine can turn it into room rows (item 16).
func TestWritePublishesOnePRStateEvent(t *testing.T) {
	root := newRoot(t)
	db, err := open(root)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []hub.Event
	appstate.For(db).SetPublish(func(ev hub.Event) { mu.Lock(); got = append(got, ev); mu.Unlock() })
	st := newState()
	st.Records["k"] = &Record{Key: "k", RoomID: "room_1", State: "open"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Topic != Topic {
		t.Fatalf("published %+v; want one %q event", got, Topic)
	}
	keys, err := KeysChangedSince(root, "0")
	if err != nil || len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("keys changed since 0 = %v, %v; want [k]", keys, err)
	}
}
