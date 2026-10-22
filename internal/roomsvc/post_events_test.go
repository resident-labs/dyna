package roomsvc

import (
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// One post reaches clients as one posts event, with its body: the room
// watch's. The room log append through internal/state publishes nothing
// until item 16 moves the posts topic to the write (requirement 13).
func TestOnePostIsOnePostsEvent(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	if _, _, err := e.svc.rooms().SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	e.mgr.Spawner = checkedTaskSpawner{next: e.mgr.Spawner, before: func() {
		t.Fatal("post-event fixture started a runner")
	}}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	var published []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) {
		if ev.Topic == "posts" {
			published = append(published, ev)
		}
	})
	t.Cleanup(func() { state.For(db).SetPublish(nil) })
	offsets, members, pins := map[string]int64{}, newMemberWatch(), map[string]string{}
	signature := ""
	e.svc.scan(offsets, &signature, members, pins, true, func(Event) {})
	// The external seat keeps the real owner post out of the internal root
	// runner. Runner delivery is independent of the watch's posts event.
	if _, err := e.svc.Post(e.ctx, "alpha", "@root hello", ""); err != nil {
		t.Fatal(err)
	}
	var posts []Event
	for range 2 {
		e.svc.scan(offsets, &signature, members, pins, false, func(event Event) {
			if event.Kind == "posts" {
				posts = append(posts, event)
			}
		})
	}
	if len(posts) != 1 || len(posts[0].Posts) != 1 || posts[0].Posts[0].Text != "hello" {
		t.Fatalf("posts events %+v, want the one post with its body", posts)
	}
	if len(published) != 0 {
		t.Fatalf("the write also published %+v; want the watch's event only", published)
	}
}
