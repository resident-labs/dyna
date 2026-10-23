package roomsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestTranscriptPageDoesNotCopyRetainedRoomHistory(t *testing.T) {
	e := newEnv(t)
	rec := e.room("transcript-work")
	id := "ses_20261021-000001-aaaaaaaa"
	base := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	meta := subagent.Meta{ID: id, Name: "lead", Room: rec.Name, RoomID: rec.ID, Turns: 3, Created: base}
	recordtest.Write(t, e.mgr.Store.Root, id, meta)
	if err := subagent.Migrate(filepath.Dir(e.mgr.Store.Root)); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		data, err := json.Marshal(subagent.Turn{N: n, Status: subagent.TurnOK, Started: base.Add(time.Duration(n) * time.Minute), Ended: base.Add(time.Duration(n)*time.Minute + 30*time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		seedTurnRow(t, e.mgr.Store, id, n, data)
	}
	const retained = 3200
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for i := range retained {
		address := rec.Name + ":peer"
		if i%2 == 0 {
			address = meta.Address()
		}
		msg := room.Message{ID: fmt.Sprintf("msg_%016x", i), TS: base.Add(time.Minute).UnixMilli(), From: address, To: []string{rec.Name}, Text: "retained post"}
		if err := enc.Encode(msg); err != nil {
			t.Fatal(err)
		}
	}
	selected := room.Message{
		ID: "msg_ffffffffffffffff", TS: base.Add(3*time.Minute + time.Second).UnixMilli(),
		From: meta.Address(), Conversation: room.DMConversation(id), To: []string{rec.Name}, Text: "in the selected turn",
	}
	if err := enc.Encode(selected); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl")
	if err := os.WriteFile(path, log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// A log written behind the store's back gets its rows the way an engine
	// start does: one import of every retained post.
	if err := room.Migrate(filepath.Dir(e.svc.rooms().Root)); err != nil {
		t.Fatal(err)
	}
	read := func() Session {
		t.Helper()
		got, err := e.svc.TranscriptPage(id, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Turns) != 1 || got.Turns[0].N != 3 || len(got.Posts) != 1 || got.Posts[0].ID != selected.ID {
			t.Fatalf("selected page: %+v", got)
		}
		return got
	}
	read() // Warm the shared log index and transcript loader.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	read()
	runtime.ReadMemStats(&after)
	// Copying the room log costs at least one Message per retained post.
	// A warm one-turn request must stay below half that work.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > retained*uint64(unsafe.Sizeof(room.Message{}))/2 {
		t.Fatalf("one transcript page allocated %d bytes for %d retained room posts", allocated, retained)
	}
	// A post the store appends while the DM is open takes its place at once, and
	// the page still reads only the window, not the retained log.
	e.svc.Notify.Rooms.Now = func() time.Time { return base.Add(3*time.Minute + 2*time.Second) }
	appended, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: meta.Address(), Conversation: room.DMConversation(id), To: []string{rec.Name}, Text: "appended in the selected turn"})
	e.svc.Notify.Rooms.Now = nil
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.TranscriptPage(id, "", 1)
	if err != nil || len(got.Posts) != 2 || got.Posts[1].ID != appended.ID {
		t.Fatalf("appended page: %+v, %v", got, err)
	}
}
