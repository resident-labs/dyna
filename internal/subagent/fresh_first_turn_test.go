package subagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// A first turn that ended before the harness reported its session leaves no
// HarnessSession. Every path that stops a turn in that gap stores the same
// record, so one fixture covers a canceled first turn and a stop in the gap
// between StatusRunning and the harness session ID.
func stoppedBeforeHarnessSession(t *testing.T, e *testEnv, status string) Info {
	t.Helper()
	id := NewID()
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	meta := Meta{ID: id, Name: "fixture-" + id, RoomID: e.room.ID, Room: e.room.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now,
		Turns: 1, Resumable: true}
	if err := e.m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	cutoff, err := e.m.rooms().FileSize(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: status, Profile: "mock",
		Prompt: "RESPOND: first", Created: now, Started: now, Ended: now,
		InputCutoff: &cutoff}); err != nil {
		t.Fatal(err)
	}
	info, err := e.m.Store.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestPostAfterTurnStoppedBeforeHarnessStartsFreshTurn(t *testing.T) {
	for _, status := range []string{TurnStopped, TurnError} {
		t.Run(status, func(t *testing.T) {
			e := newTestEnv(t)
			info := stoppedBeforeHarnessSession(t, e, status)
			if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true,
				From: info.Room, To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: again"}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.TickSessions(e.ctx); err != nil {
				t.Fatal(err)
			}
			got, turn := e.wait(info.ID, 2)
			if turn.Status != TurnOK {
				t.Fatalf("fresh turn status = %q, error %q", turn.Status, turn.Error)
			}
			if got.HarnessSession == "" || turn.HarnessSession == "" {
				t.Fatal("the fresh turn recorded no harness session")
			}
			if len(turn.Steering) != 1 {
				t.Fatalf("fresh turn did not read the post: %+v", turn.Steering)
			}
		})
	}
}

// Start publishes the session record before it allocates turn 1. A post that
// arrives in that gap must not take that turn: the initial brief and schema
// belong to it.
func TestPostDoesNotAllocateTheFirstTurnOfACreatingSession(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// The record Start publishes before it allocates its first turn.
	meta := Meta{ID: id, Name: "fixture-" + id, RoomID: e.room.ID, Room: e.room.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now, Resumable: true}
	if err := e.m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	msg, err := e.m.rooms().Post(e.room.ID, room.PostInput{Wake: true, From: e.room.Name,
		To: []string{meta.Address()}, Text: "SLEEP:0\nRESPOND: post in the gap"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := e.m.Store.ReadMeta(id); err != nil || got.Turns != 0 {
		t.Fatalf("the post allocated turn %d: %v", got.Turns, err)
	}
	if _, err := e.m.Wake(e.ctx, id, WakeOptions{MessageID: msg.ID}); !errors.Is(err, ErrBusy) {
		t.Fatalf("Wake = %v, want ErrBusy while the first turn is not allocated", err)
	}
	// The original start still owns turn 1, with its brief and schema.
	turn, delivered, err := e.m.beginTurnConfigured(id, "SLEEP:0\nRESPOND: first brief", `{"type":"object"}`, []string{e.room.Name}, "", "", false, false, time.Time{})
	if err != nil || delivered {
		t.Fatalf("initial turn = %+v, delivered = %v, %v", turn, delivered, err)
	}
	if turn.N != 1 || turn.Prompt != "SLEEP:0\nRESPOND: first brief" || turn.Schema == "" {
		t.Fatalf("initial turn lost its brief or schema: %+v", turn)
	}
	if got, err := e.m.Store.ReadMeta(id); err != nil || got.Turns != 1 {
		t.Fatalf("turn counter = %d, %v; want 1", got.Turns, err)
	}
}
