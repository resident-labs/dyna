package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/room"
)

type failingRoomSizer struct{ err error }

func (f failingRoomSizer) FileSize(string) (int64, error) { return 0, f.err }

type fixedRoomSizer struct{ size int64 }

func (f fixedRoomSizer) FileSize(string) (int64, error) { return f.size, nil }

// A room-log measurement failure must not drop a completed turn. The runner
// keeps trying, then sends every terminal fact; the engine measures the
// cutoff when it applies them. The result, usage and one completion survive.
func TestCutoffFailureKeepsTheCompletedTurn(t *testing.T) {
	e := newTestEnv(t)
	var completions atomic.Int32
	e.m.TurnReporter = func(context.Context, *Manager, string, int) error {
		completions.Add(1)
		return nil
	}
	failures := cutoffAttempts
	e.m.RoomFileSize = func(roomID string) (int64, error) {
		if failures > 0 {
			failures--
			return 0, errors.New("room log unavailable")
		}
		return e.m.rooms().FileSize(roomID)
	}
	info, _ := e.start("SLEEP:0\nRESPOND: done")
	_, turn := e.wait(info.ID, 1)
	if turn.Status != TurnOK {
		t.Fatalf("turn status = %q, error %q", turn.Status, turn.Error)
	}
	if fmt.Sprint(turn.Result) != "done" {
		t.Fatalf("result = %#v, want the harness output", turn.Result)
	}
	if turn.InputCutoff == nil {
		t.Fatal("the completed turn was stored without an input cutoff")
	}
	if turn.Usage.Total() == 0 {
		t.Fatal("the completed turn lost its usage")
	}
	if got := completions.Load(); got != 1 {
		t.Fatalf("completions = %d, want one", got)
	}
}

// runningTurnFixture stores a session whose first turn is running, so a
// terminal writer can end it.
func runningTurnFixture(t *testing.T, e *testEnv) string {
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
	if err := e.m.Store.writeMeta(Meta{ID: id, Name: "fixture-" + id, RoomID: e.room.ID, Room: e.room.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now,
		Turns: 1, Resumable: true, HarnessSession: "mock-" + id, Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A terminal write that carries no cutoff gets one from the engine, so an
// older runner cannot store a terminal turn without its input boundary.
func TestEndTurnMeasuresAMissingCutoff(t *testing.T) {
	e := newTestEnv(t)
	id := runningTurnFixture(t, e)
	if err := e.m.endTurn(e.ctx, id, TurnEnd{Turn: 1, Status: TurnOK, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(id, 1)
	if err != nil || turn.Status != TurnOK || turn.InputCutoff == nil {
		t.Fatalf("turn = %q cutoff %v, %v; want a terminal turn with a cutoff", turn.Status, turn.InputCutoff, err)
	}
}

// A pending cutoff uses the runner's read position, not a later EOF, so input
// that arrived after the turn ended stays eligible for a retry.
func TestEndTurnUsesTheFallbackCutoff(t *testing.T) {
	e := newTestEnv(t)
	id := runningTurnFixture(t, e)
	e.m.RoomFileSize = func(string) (int64, error) { return 9999, nil }
	fallback := int64(42)
	if err := e.m.endTurn(e.ctx, id, TurnEnd{Turn: 1, Status: TurnError, Ended: time.Now(),
		CutoffPending: true, FallbackCutoff: &fallback}); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(id, 1)
	if err != nil || turn.InputCutoff == nil || *turn.InputCutoff != fallback {
		t.Fatalf("cutoff = %v, %v; want the read position %d", turn.InputCutoff, err, fallback)
	}
}

// Without a fallback the engine must measure the cutoff, and a failed measure
// must be retryable so the outbox keeps the terminal request and its facts.
func TestCutoffFailureWithoutAFallbackIsRetryable(t *testing.T) {
	e := newTestEnv(t)
	id := runningTurnFixture(t, e)
	e.m.RoomFileSize = func(string) (int64, error) { return 0, errors.New("room log unavailable") }
	err := e.m.endTurn(e.ctx, id, TurnEnd{Turn: 1, Status: TurnError, Ended: time.Now(), CutoffPending: true})
	if err == nil || !errors.Is(err, callspec.ErrRetryable) {
		t.Fatalf("endTurn error = %v, want a retryable cutoff failure", err)
	}
	turn, readErr := e.m.Store.ReadTurn(id, 1)
	if readErr != nil || turn.Status == TurnError {
		t.Fatalf("turn = %q, %v; want it uncommitted", turn.Status, readErr)
	}
}

// An undelivered live claim stays pending without waking the failed turn when
// it lies at or before the turn's final boundary, and a post after that
// boundary still wakes the session.
func TestFailedTurnBoundaryKeepsOnlyNewerInput(t *testing.T) {
	e := newTestEnv(t)
	id := runningTurnFixture(t, e)
	meta, err := e.m.Store.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	old, err := e.m.rooms().Post(e.room.ID, room.PostInput{Wake: true, From: e.room.Name,
		To: []string{meta.Address()}, Text: "SLEEP:0\nRESPOND: old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.startTurn(e.ctx, id, TurnStart{Turn: 1, Running: true,
		Drained: []DrainedPost{{Steer: Steer{ID: old.ID, Message: old.Text}}}}); err != nil {
		t.Fatal(err)
	}
	claimPostsFor(t, e, id, 1, old.ID)
	// The runner dies before the harness takes the prompt: the claim is
	// delivered by nobody, and the repair fixes the boundary at the old post.
	if _, err := e.m.Store.RepairTurn(id, 1, "runner exited without recording a result"); err != nil {
		t.Fatal(err)
	}
	newer, err := e.m.rooms().Post(e.room.ID, room.PostInput{Wake: true, From: e.room.Name,
		To: []string{meta.Address()}, Text: "SLEEP:0\nRESPOND: newer"})
	if err != nil {
		t.Fatal(err)
	}
	meta, err = e.m.Store.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.m.pendingWakePosts(meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != newer.ID {
		t.Fatalf("posts = %v, want only the post after the boundary %s", posts, newer.ID)
	}
}

func TestTerminalCutoffMeasuresTheRoomLog(t *testing.T) {
	cutoff, err := terminalCutoff(fixedRoomSizer{size: 42}, "room_x")
	if err != nil || cutoff == nil || *cutoff != 42 {
		t.Fatalf("terminalCutoff = %v, %v; want 42", cutoff, err)
	}
}

func TestTerminalCutoffRefusesAnUnmeasuredRoomLog(t *testing.T) {
	cutoff, err := terminalCutoff(failingRoomSizer{err: errors.New("room log unavailable")}, "room_x")
	if err == nil || cutoff != nil {
		t.Fatalf("terminalCutoff = %v, %v; want no cutoff and an error", cutoff, err)
	}
}

// A repair that cannot measure the room log must not store a terminal turn:
// without a cutoff a later wake could retry input the turn already read. The
// room stamp is complete, so only the log measurement fails.
func TestRepairTurnDoesNotStoreATurnWithoutAnInputCutoff(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeMeta(Meta{ID: id, RoomID: "not/a/room", Room: "work", Turns: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	repaired, err := s.RepairTurn(id, 1, "runner exited without recording a result")
	if err == nil || repaired {
		t.Fatalf("RepairTurn = %v, %v; want a refusal", repaired, err)
	}
	if !strings.Contains(err.Error(), "measure the terminal input cutoff") {
		t.Fatalf("RepairTurn error = %v, want the cutoff measurement", err)
	}
	turn, err := s.ReadTurn(id, 1)
	if err != nil || turn.Status != TurnRunning || turn.InputCutoff != nil {
		t.Fatalf("turn = %q cutoff %v, %v; want it untouched", turn.Status, turn.InputCutoff, err)
	}
}
