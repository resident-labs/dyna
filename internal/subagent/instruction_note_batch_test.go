package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// A note for many sessions commits in one write, so it publishes one session
// event. Every eligible session still gets the quiet-period start time.
func TestInstructionNoteWritesManySessionsInOneTransaction(t *testing.T) {
	e := newTestEnv(t)
	const sessions = 40
	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("ses_note%02d", i)
		dir, err := e.m.Store.Dir(ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := Meta{ID: ids[i], RoomID: e.room.ID, Room: e.room.Name, Instructions: &instructions.Snapshot{}}
		if err := e.m.Store.writeMeta(meta); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []hub.Event
	writer := state.For(db)
	writer.SetPublish(func(ev hub.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})
	t.Cleanup(func() { writer.SetPublish(func(hub.Event) {}) })

	reached := e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	writer.WaitPublished()

	if len(reached) != sessions {
		t.Fatalf("reached %d sessions, want %d", len(reached), sessions)
	}
	for _, id := range ids {
		meta, err := e.m.Store.Info(id)
		if err != nil || meta.InstructionsChangedAt.IsZero() {
			t.Fatalf("session %s has no quiet period start: %+v, %v", id, meta, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Topic != sessionlife.Topic || events[0].Key != "" {
		t.Fatalf("published %d events, want one session event for all sessions: %+v", len(events), events)
	}
}
