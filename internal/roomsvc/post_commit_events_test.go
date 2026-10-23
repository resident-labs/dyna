package roomsvc

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestCommittedPostsAcrossRoomsAreDeliveredOnce(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("alpha"), e.room("beta")
	db, err := database.Open(filepath.Dir(e.svc.rooms().Root))
	if err != nil {
		t.Fatal(err)
	}
	var committed []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) {
		if ev.Topic == "room-post" {
			committed = append(committed, ev)
		}
	})
	defer state.For(db).SetPublish(nil)
	e.say(a, "writer", "first")
	e.say(b, "writer", "second")
	if len(committed) != 2 {
		t.Fatalf("committed post events = %d; want 2", len(committed))
	}
	offsets := map[string]int64{}
	var delivered []Event
	emit := func(ev Event) { delivered = append(delivered, ev) }
	for _, ev := range committed {
		e.svc.PostsChanged(ev.Room)
	}
	e.svc.StoreChanged("goals")
	e.svc.takeStoreTopics() // Draining store kinds must preserve room IDs.
	if len(e.svc.storeWake) != 1 {
		t.Fatal("post wakes did not coalesce")
	}
	<-e.svc.storeWake
	e.svc.scanChangedPosts(offsets, emit)
	for _, rec := range []string{a.ID, b.ID} {
		e.svc.scanPosts(rec, offsets, false, emit)
	}
	if len(delivered) != 2 {
		t.Fatalf("delivered %d events; want 2", len(delivered))
	}
	seen := map[string]bool{}
	for _, ev := range delivered {
		if ev.Kind != "posts" || len(ev.Posts) != 1 || seen[ev.RoomID] {
			t.Fatalf("unexpected delivery: %+v", ev)
		}
		seen[ev.RoomID] = true
		size, err := e.svc.rooms().FileSize(ev.RoomID)
		if err != nil || ev.Offset != size || offsets[ev.RoomID] != size {
			t.Fatalf("wrong offset: %+v, size %d, err %v", ev, size, err)
		}
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatal("missing room")
	}

}
