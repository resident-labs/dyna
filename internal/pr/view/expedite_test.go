package view_test

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestQueueViewChangesOnExpediteAndExpiryWithoutPRWrites(t *testing.T) {
	e := newEnv(t)
	room := e.room("expedite-cache")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	e.svc.PRs.Now = func() time.Time { return now }
	root := e.svc.PRs.Watcher().Store.Root
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"a": {Key: "a", RoomID: room.ID, Head: "head-a", State: "open", Watching: true, CI: "passed"},
		"b": {Key: "b", RoomID: room.ID, Head: "head-b", State: "open", Watching: true, CI: "passed", GoalID: "g1"},
	}})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	changes := 0
	scan := func() {
		e.svc.PRs.Scan(signatures, work, &version, false, func(_ string, _ string, change *listchange.Change, _ bool) {
			if change != nil {
				changes++
			}
		})
	}
	assertPosition := func(want int) {
		t.Helper()
		detail, err := e.svc.PRs.ChangeDetail(room.ID, "b")
		if err != nil {
			t.Fatal(err)
		}
		if detail.QueuePosition == nil {
			t.Fatal("queue position is missing")
		}
		if *detail.QueuePosition != want {
			t.Fatalf("queue position=%d, want %d", *detail.QueuePosition, want)
		}
	}
	scan()
	assertPosition(2)
	before := changes
	until := now.Add(time.Hour)
	store := &goals.Store{Root: filepath.Join(filepath.Dir(root), "goals"), Now: func() time.Time { return now }}
	if err := store.Update(func(st *goals.State) error {
		st.Room(room.ID).Goals["g1"] = &goals.Goal{ID: "g1", Room: room.ID, State: "active", ExpediteUntil: &until}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scan()
	assertPosition(1)
	if changes == before {
		t.Fatal("expediting a goal emitted no changed PR view")
	}
	before = changes
	now = until
	scan()
	assertPosition(2)
	if changes == before {
		t.Fatal("expiry emitted no changed PR view")
	}
}
