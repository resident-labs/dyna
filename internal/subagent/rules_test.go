package subagent

import (
	"dyna-agent/internal/instructions"
	"strings"
	"testing"
	"time"
)

func TestInstructionNoteSteersTheRunningTurn(t *testing.T) {
	e := newTestEnv(t)
	started := make(chan struct{}, 1)
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" {
			started <- struct{}{}
		}
	}
	info, _ := e.start("SLEEP:60000\nRESPOND: waiting for changed instructions")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("mock turn did not start")
	}
	if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "Check the running note."); err != nil {
		t.Fatal(err)
	}
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	if _, err := e.m.FlushInstructionNotes(e.ctx, time.Now().Add(instructionQuietPeriod)); err != nil {
		t.Fatal(err)
	}
	spawner := e.m.Spawner.(*joinedFixtureSpawner)
	finished, ok := spawner.turns.Load(fixtureTurn{info.ID, 1})
	if !ok {
		t.Fatal("running turn has no completion event")
	}
	select {
	case <-finished.(chan struct{}):
	case <-time.After(3 * time.Second):
		t.Fatal("instruction note did not reach the running mock turn")
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.Status != TurnOK || len(turn.Steering) != 1 || !turn.Steering[0].Delivered || !strings.Contains(turn.Steering[0].Message, "Check the running note.") {
		t.Fatalf("running turn did not receive its note: %+v, %v", turn, err)
	}
	note, err := e.m.rooms().Message(info.RoomID, turn.Steering[0].ID)
	if err != nil || note.Wake || note.Kind != "instructions" {
		t.Fatalf("instruction note: %+v, %v", note, err)
	}
	if meta, err := e.m.Store.ReadMeta(info.ID); err != nil || meta.Turns != 1 {
		t.Fatalf("instruction note started another turn: %+v, %v", meta, err)
	}
}

// Cached reads return copies, so a caller that changes a returned snapshot
// does not change the next read.
func TestCachedMetaCopiesInstructions(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: hi")
	e.wait(info.ID, 1)
	snap := instructions.Build(instructions.Section{Name: "Room rules", Text: "rules", Items: []string{"r1 one"}})
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Instructions, m.InstructionsKnown = &snap, &snap }); err != nil {
		t.Fatal(err)
	}
	first, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*instructions.Snapshot{first.Instructions, first.InstructionsKnown} {
		s.Hash = "changed"
		s.Sections[0].Items[0] = "changed"
	}
	again, _ := e.m.Store.ReadMeta(info.ID)
	for _, s := range []*instructions.Snapshot{again.Instructions, again.InstructionsKnown} {
		if s.Hash != snap.Hash || s.Sections[0].Items[0] != "r1 one" {
			t.Fatalf("a caller changed the cached snapshot: %+v", s)
		}
	}
}

// Before a session's quiet period ends, a flush posts nothing and returns the
// due time, so the engine can wake exactly then instead of every second.
func TestFlushReturnsWhenTheNextInstructionNoteIsDue(t *testing.T) {
	e := newTestEnv(t)
	started := make(chan struct{}, 1)
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	}
	info, _ := e.start("SLEEP:60000\nRESPOND: waiting")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("mock turn did not start")
	}
	if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "Wait for the quiet period."); err != nil {
		t.Fatal(err)
	}
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	meta, err := e.m.Store.Info(info.ID)
	if err != nil || meta.InstructionsChangedAt.IsZero() {
		t.Fatalf("no quiet period started: %+v, %v", meta, err)
	}
	next, err := e.m.FlushInstructionNotes(e.ctx, meta.InstructionsChangedAt)
	if err != nil || !next.Equal(meta.InstructionsChangedAt.Add(instructionQuietPeriod)) {
		t.Fatalf("next = %v, %v; want %v", next, err, meta.InstructionsChangedAt.Add(instructionQuietPeriod))
	}
	if after, _ := e.m.Store.Info(info.ID); after.InstructionsChangedAt.IsZero() {
		t.Fatal("a note was sent before its quiet period ended")
	}
	if next, err := e.m.FlushInstructionNotes(e.ctx, next); err != nil || !next.IsZero() {
		t.Fatalf("after the note: next = %v, %v; want zero", next, err)
	}
}

// A Manager with the engine's hook calls it when it stores a quiet period.
func TestNoteRoomInstructionsCallsTheEngineHook(t *testing.T) {
	e := newTestEnv(t)
	called := 0
	e.m.InstructionsChanged = func() { called++ }
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	if called != 1 {
		t.Fatalf("hook called %d times, want 1", called)
	}
}
