package subagent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// A runner owns its turn lock through synchronous process retirement. Capture
// that lock so the test can deterministically exercise both sides of the
// protocol seal without timing a process or invoking a real model.
type heldTurnSpawner struct{ lock *os.File }

func (s *heldTurnSpawner) Spawn(_ *Manager, _ string, _ int, lock *os.File) error {
	s.lock = lock
	return nil
}

func TestTerminalSealRoutesLatePostToExactFollowup(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: initial")
	_, first := e.wait(info.ID, 1)
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	second, err := e.m.beginTurn(info.ID, "RESPOND: active", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	// Future turns use the normal fake runner, while this one still holds its
	// lock as it would during retirement of a lingering Claude process.
	// Restore the joined fixture so e.wait includes retirement writes and
	// cleanup joins the follow-up before removing the temporary store.
	e.m.Spawner = runner
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := e.m.Store.newTurnGate(e.m.RoomStore(), meta, second.N, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(t *Turn) { t.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}

	before, err := e.send(info.ID, "SLEEP:1 RESPOND: before seal", WakeOptions{})
	if err != nil || !before.Steered || before.Turn != second.N {
		t.Fatalf("before seal=%#v, %v", before, err)
	}
	messages, _, err := gate.Read(0, true)
	if err != nil || len(messages) != 1 {
		t.Fatalf("final read lost admitted input: %#v, %v", messages, err)
	}
	if _, open, err := e.m.Store.gateOpen(info.ID); err != nil || !open {
		t.Fatalf("nonempty final read sealed gate: %v, %v", open, err)
	}
	// The harness acknowledges the final input before its covering result.
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(t *Turn) {
		for _, msg := range messages {
			t.Steering = append(t.Steering, Steer{ID: msg.ID, TS: msg.TS, Message: msg.Message, Delivered: true})
		}
	}); err != nil {
		t.Fatal(err)
	}
	// This represents the covering result after the just-admitted steering.
	if messages, _, err := gate.Read(0, true); err != nil || len(messages) != 0 {
		t.Fatalf("sealing read: %#v, %v", messages, err)
	}
	closedAt, ok := e.m.Store.closedAt(info.ID, second.N)
	if !ok {
		t.Fatal("protocol boundary did not durably seal the gate")
	}

	after, err := e.m.RoomStore().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "RESPOND: after seal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Wake(e.ctx, info.ID, WakeOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("closed gate must defer the post: %v", err)
	}
	if _, open, err := e.m.Store.gateOpen(info.ID); err != nil || open {
		t.Fatalf("sealed transport advertised live steering: %v, %v", open, err)
	}
	// Commit/release only after retirement, as RunTurn does. The turn-end
	// event must route late input to a later turn, never to this closed one.
	if _, err := e.m.Store.UpdateTurn(info.ID, second.N, func(t *Turn) { t.Status = TurnOK; t.Result = "sealed"; t.Ended = time.Now() }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.RoomCursor = closedAt }); err != nil {
		t.Fatal(err)
	}
	if err := held.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.m.TickSessionTargets(e.ctx, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	_, followup := e.wait(info.ID, second.N+1)
	if followup.Status != TurnOK || followup.HarnessSession != first.HarnessSession || !strings.Contains(followup.Prompt, "after seal") || strings.Contains(followup.Prompt, "before seal") {
		t.Fatalf("follow-up did not resume exact session with only late input: %#v", followup)
	}
	if len(followup.Steering) != 1 || followup.Steering[0].ID != after.ID || !followup.Steering[0].Delivered {
		t.Fatalf("late post not delivered exactly once: %#v", followup.Steering)
	}
}

// TestLockProbesDoNotSeeEachOther: concurrent status reads of an idle
// session never report it running.
func TestLockProbesDoNotSeeEachOther(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	id := NewID()
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turn.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var held atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if store.lockHeld(id) {
					held.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := held.Load(); n != 0 {
		t.Fatalf("idle session probed as running %d times", n)
	}
	f, err := store.tryTurnLock(id)
	if err != nil || f == nil {
		t.Fatalf("tryTurnLock = %v, %v", f, err)
	}
	defer f.Close()
	if !store.lockHeld(id) {
		t.Fatal("a held turn lock must probe as running")
	}
}
