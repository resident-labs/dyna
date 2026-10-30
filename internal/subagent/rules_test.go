package subagent

import (
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/room"
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
	if err != nil || note.Wake || note.Kind != "session" || note.Notice.Kind != "instructions" {
		t.Fatalf("instruction note: %+v, %v", note, err)
	}
	if meta, err := e.m.Store.ReadMeta(info.ID); err != nil || meta.Turns != 1 {
		t.Fatalf("instruction note started another turn: %+v, %v", meta, err)
	}
}

// Cached reads return copies, so a caller that changes a returned snapshot
// does not change the next read.
func TestRuleFanoutReplaysAfterInterruptedSave(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	snapshot := instructions.Build(instructions.Section{Name: "Room rules", Text: "rules"})
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Instructions = &snapshot }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "A durable rule."); err != nil {
		t.Fatal(err)
	}
	// Simulate an exit after the rule commits, before its fan-out starts.
	if _, err := e.m.FlushInstructionNotes(e.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.Info(info.ID)
	if err != nil || meta.InstructionsChangedAt.IsZero() {
		t.Fatalf("replay did not mark the session: %+v, %v", meta, err)
	}
}

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

// A change that reverts to an earlier state is a new note, and a retry of one
// change stays one note. Without an occurrence the second change would reuse
// the first post and the session would keep the reverted instructions.
func TestInstructionNotesRepeatAfterARevertedChange(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	flush := func() room.Message {
		t.Helper()
		if _, err := e.m.FlushInstructionNotes(e.ctx, time.Now().Add(instructionQuietPeriod)); err != nil {
			t.Fatal(err)
		}
		meta, err := e.m.Store.ReadMeta(info.ID)
		if err != nil || len(meta.InstructionsNotes) == 0 {
			t.Fatalf("no instruction note: %v", err)
		}
		note, err := e.m.rooms().Message(info.RoomID, meta.InstructionsNotes[len(meta.InstructionsNotes)-1])
		if err != nil {
			t.Fatal(err)
		}
		return note
	}
	// A rule appears, disappears, and appears again: the third change must
	// reach the session even though it repeats the first.
	rule, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "Check the running note.")
	if err != nil {
		t.Fatal(err)
	}
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	first := flush()
	if _, err := e.m.goalStore().RemoveRule(e.room.ID, rule.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	second := flush()
	if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "Check the running note."); err != nil {
		t.Fatal(err)
	}
	e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	third := flush()
	if first.ID == second.ID || second.ID == third.ID || first.ID == third.ID {
		t.Fatalf("notes reused a post: %s, %s, %s", first.ID, second.ID, third.ID)
	}
	payload, ok := third.Session()
	if !ok || payload.Occurrence != 2 || len(payload.Sections) != 1 || len(payload.Sections[0].Added) != 1 || !strings.Contains(payload.Sections[0].Added[0], "Check the running note.") {
		t.Fatalf("third note = %+v, %v", payload, ok)
	}
	// Retrying one occurrence keeps its post: the same change posts once.
	before := third.ID
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.InstructionsChangedAt = time.Time{} }); err != nil {
		t.Fatal(err)
	}
	again := flush()
	if again.ID != before {
		t.Fatalf("a retry of one change posted %s after %s", again.ID, before)
	}
}

// A snapshot refresh covers the notes it includes and clears their list, but it
// must not reset the occurrence: a change that repeats an earlier one after the
// refresh is a new note, or the session would miss it.
func TestInstructionNoteRepeatsAfterASnapshotRefresh(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	flush := func() room.Message {
		t.Helper()
		if _, err := e.m.FlushInstructionNotes(e.ctx, time.Now().Add(instructionQuietPeriod)); err != nil {
			t.Fatal(err)
		}
		meta, err := e.m.Store.ReadMeta(info.ID)
		if err != nil || len(meta.InstructionsNotes) == 0 {
			t.Fatalf("no instruction note: %v", err)
		}
		note, err := e.m.rooms().Message(info.RoomID, meta.InstructionsNotes[len(meta.InstructionsNotes)-1])
		if err != nil {
			t.Fatal(err)
		}
		return note
	}
	change := func(add bool) {
		t.Helper()
		if add {
			if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "Check the running note."); err != nil {
				t.Fatal(err)
			}
		} else {
			rules, err := e.m.goalStore().Rules(e.room.ID)
			if err != nil || len(rules) == 0 {
				t.Fatalf("rules = %+v, %v", rules, err)
			}
			if _, err := e.m.goalStore().RemoveRule(e.room.ID, rules[len(rules)-1].ID, "owner"); err != nil {
				t.Fatal(err)
			}
		}
		e.m.NoteRoomInstructions(e.ctx, e.room.ID)
	}
	change(true)
	first := flush()
	change(false)
	flush()
	// A refresh folds the notes into a new frozen snapshot, as a compaction or
	// a transfer does: the covered notes leave the list.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
		meta.Instructions = meta.InstructionsKnown
		meta.InstructionsStale = false
		meta.InstructionsKnown = nil
		meta.InstructionsNotes = nil
	}); err != nil {
		t.Fatal(err)
	}
	change(true)
	third := flush()
	if third.ID == first.ID {
		t.Fatalf("the change after a refresh reused post %s", first.ID)
	}
	payload, ok := third.Session()
	if !ok || payload.Occurrence == 0 {
		t.Fatalf("third note = %+v, %v", payload, ok)
	}
}
