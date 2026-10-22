package subagent

import (
	"context"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionWorkerDoesNotRetryFailedInputUntilFreshPost(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	post := func() room.Message {
		t.Helper()
		msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: retry"})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	old := post()
	cutoff, _ := e.m.rooms().FileSize(info.RoomID)
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.Status = TurnError; turn.InputCutoff = &cutoff }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("failed turn retried old input")
	}
	fresh := post()
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(info.ID, 2)
	if len(turn.Steering) != 2 || turn.Steering[0].ID != old.ID || turn.Steering[1].ID != fresh.ID {
		t.Fatalf("fresh post lost queued input: %+v", turn.Steering)
	}
}

func TestSessionWorkerWrapUpRunsAfterCompletion(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string { return []string{"Finish the open question."} }}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(info.ID, 2)
	if !turn.WrapUp {
		t.Fatal("engine did not start the wrap-up")
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 2 {
		t.Fatal("engine repeated the wrap-up")
	}
}

func TestSessionWorkerWaitsForTransferAndRoomResume(t *testing.T) {
	for _, gate := range []string{"transfer", "pause"} {
		t.Run(gate, func(t *testing.T) {
			e := newTestEnv(t)
			info, _ := e.start("SLEEP:0\nRESPOND: ready")
			e.wait(info.ID, 1)
			if gate == "transfer" {
				if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Transfer = &TransferState{ID: "test", Phase: "starting"} }); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := e.m.rooms().SetSessionsPaused(e.ctx, info.RoomID, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: queued"}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.TickSessions(e.ctx); err != nil {
				t.Fatal(err)
			}
			meta, _ := e.m.Store.ReadMeta(info.ID)
			if meta.Turns != 1 {
				t.Fatalf("worker bypassed %s", gate)
			}
			if gate == "transfer" {
				if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Transfer.Phase = "completed" }); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := e.m.rooms().SetSessionsPaused(e.ctx, info.RoomID, false); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.m.TickSessions(e.ctx); err != nil {
				t.Fatal(err)
			}
			_, turn := e.wait(info.ID, 2)
			if len(turn.Steering) != 1 || !turn.Steering[0].Delivered {
				t.Fatalf("lost gated post: %+v", turn)
			}
		})
	}
}

func TestSessionWorkerNoWakeStillReachesRunningGate(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	turn, err := e.m.beginTurn(info.ID, "active", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	meta, _ := e.m.Store.ReadMeta(info.ID)
	gate := e.m.Store.newTurnGate(e.m.rooms(), meta, turn.N, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: false, From: info.Room, To: []string{info.Address()}, Text: "instruction note"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	messages, _, err := gate.Read(0, false)
	if err != nil || len(messages) != 1 || messages[0].ID != msg.ID {
		t.Fatalf("NoWake input did not reach active gate: %+v %v", messages, err)
	}
	current, _ := e.m.Store.ReadMeta(info.ID)
	if current.Turns != turn.N {
		t.Fatal("NoWake started an extra turn")
	}
}

func TestSessionWorkerCancelledPostDoesNotStartTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "cancel this"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.DeletePendingSteer(e.m.rooms(), info.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("cancelled input started a turn")
	}
}

func TestSessionWorkerCancellationAfterScanDoesNotStartTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "cancel before allocation"})
	if err != nil {
		t.Fatal(err)
	}
	e.m.checkpoint = func(stage string) {
		if stage == "wake-selected" {
			if err := e.m.Store.DeletePendingSteer(e.m.rooms(), info.ID, msg.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("cancelled selection started an empty turn")
	}
}

func TestGoalDeliveryStoresInputUntilEngineRuns(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	var post room.Message
	stored, err := e.m.DeliverGoal(e.ctx, info.ID, func() (string, error) {
		var err error
		post, err = e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, Kind: "goal", From: "goal-keeper", To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: goal update"})
		return post.ID, err
	})
	if err != nil || !stored {
		t.Fatalf("goal delivery: %v %v", stored, err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("goal poster started a turn")
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(info.ID, 2)
	if len(turn.Steering) != 1 || turn.Steering[0].ID != post.ID || !turn.Steering[0].Delivered {
		t.Fatalf("goal input was lost: %+v", turn)
	}
}

func TestSessionWorkerNoWakeDoesNotRecheckOldWrapUp(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	// The completion pass has no reminder to send.
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string { return []string{"New state must wait for a real turn."} }}
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: false, From: info.Room, To: []string{info.Address()}, Text: "instruction note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatal("NoWake caused a wrap-up from an old completion")
	}
}

func TestSessionWorkerFindsWakeBeyondFirstRoomPage(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	path := filepath.Join(e.m.rooms().Root, info.RoomID, "messages.jsonl")
	log, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Fill more than one 4 MiB page with valid history for the room owner.
	encoder := json.NewEncoder(log)
	for i := range 300 {
		if err := encoder.Encode(room.Message{ID: fmt.Sprintf("msg_%016x", i), RoomID: info.RoomID, From: info.Room, To: []string{info.Room}, Text: strings.Repeat("x", room.MaxMessageBytes)}); err != nil {
			log.Close()
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	post, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: late input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Turns != 2 {
		t.Fatal("worker missed wake intent beyond the first room page")
	}
	_, turn := e.wait(info.ID, 2)
	if len(turn.Steering) != 1 || turn.Steering[0].ID != post.ID || !turn.Steering[0].Delivered {
		t.Fatalf("late input was not delivered: %+v", turn)
	}
}

func TestSessionWorkerDoesNotReadTurnWithoutNewInput(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.WrapUpAfter = meta.Turns }); err != nil {
		t.Fatal(err)
	}
	reads := 0
	e.m.Store.decodeTurn = func(data []byte, value any) error {
		reads++
		return json.Unmarshal(data, value)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("idle pass read %d turn files without new input", reads)
	}
}

func TestSessionTargetsDoNotReadUnrelatedTurns(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("SLEEP:0\nRESPOND: target")
	e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:0\nRESPOND: unrelated")
	e.wait(b.ID, 1)
	join := e.joinRunners()
	join()
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	t.Cleanup(func() {
		if held.lock != nil {
			held.lock.Close()
		}
	})
	if _, err := e.m.rooms().Post(a.RoomID, room.PostInput{Wake: true, From: a.Room, To: []string{a.Address()}, Text: "new input"}); err != nil {
		t.Fatal(err)
	}
	reads := map[string]int{}
	e.m.Store.decodeTurn = func(data []byte, value any) error {
		var turn Turn
		if err := json.Unmarshal(data, &turn); err != nil {
			return err
		}
		reads[turn.Prompt]++
		return json.Unmarshal(data, value)
	}
	if err := e.m.TickSessionTargets(e.ctx, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	if reads["SLEEP:0\nRESPOND: unrelated"] != 0 {
		t.Fatalf("post to A read B's turn: %v", reads)
	}
	if held.lock == nil {
		t.Fatal("target did not start its turn")
	}
}

func TestSessionTargetsFollowDormantConsolidationAlias(t *testing.T) {
	e := newTestEnv(t)
	// Alias delivery needs completed sessions, not startup harness calls.
	a := completedFixtureSession(t, e)
	b := completedFixtureSession(t, e)
	// Hold the synchronous spawn event so retirement cannot return this
	// otherwise unowned session to dormancy before we inspect the wake.
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	t.Cleanup(func() {
		if held.lock != nil {
			held.lock.Close()
		}
	})
	if _, err := e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.ConsumedBy = a.ID }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(a.ID, func(meta *Meta) { meta.Finished = time.Now(); meta.Status = StatusDormant }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.rooms().Post(a.RoomID, room.PostInput{Wake: true, From: a.Room, To: []string{a.Address()}, Text: "resume"}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.TickSessionTargets(e.ctx, []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	// Inspect only the targeted pass. A full session scan could hide a
	// failure to follow the consumed session's alias.
	meta, err := e.m.Store.ReadMeta(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Turns != 2 || !meta.Finished.IsZero() {
		t.Fatalf("alias target did not wake: %+v", meta)
	}
	if held.lock == nil {
		t.Fatal("alias target did not spawn its turn")
	}
	turn, err := e.m.Store.ReadTurn(a.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != TurnQueued {
		t.Fatalf("alias target turn was not queued: %+v", turn)
	}
}

func TestSessionRoomResumeDoesNotReadOtherRoomTurns(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("SLEEP:0\nRESPOND: room one")
	e.wait(a.ID, 1)
	other, err := e.m.rooms().Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Test other room", Profile: "mock", Name: "other", Room: other.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: other room"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(b.ID, 1)
	e.m.Store.decodeTurn = func(data []byte, value any) error {
		var turn Turn
		if err := json.Unmarshal(data, &turn); err != nil {
			return err
		}
		if turn.Prompt == "SLEEP:0\nRESPOND: other room" {
			t.Error("room resume read a turn in another room")
		}
		return json.Unmarshal(data, value)
	}
	if err := e.m.TickSessionRoom(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRecoverySkipsUnrelatedDamagedMetadata(t *testing.T) {
	for _, scope := range []string{"fallback", "room-resume"} {
		for _, damage := range []string{"malformed", "incomplete-room"} {
			t.Run(scope+"/"+damage, func(t *testing.T) {
				e := newTestEnv(t)
				info, _ := e.start("SLEEP:0\nRESPOND: ready")
				e.wait(info.ID, 1)
				if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "SLEEP:0\nRESPOND: recovered"}); err != nil {
					t.Fatal(err)
				}
				id := NewID()
				raw := []byte("{")
				if damage == "incomplete-room" {
					raw = []byte(fmt.Sprintf("{\"id\":%q,\"name\":\"damaged\",\"roomId\":\"other-room\",\"closed\":\"2026-01-01T00:00:00Z\"}", id))
				}
				recordtest.WriteRaw(t, e.m.Store.Root, id, raw)
				var err error
				if scope == "fallback" {
					_, err = e.m.TickSessions(e.ctx)
				} else {
					err = e.m.TickSessionRoom(e.ctx, info.RoomID)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, turn := e.wait(info.ID, 2)
				if turn.Status != TurnOK || len(turn.Steering) != 1 || !turn.Steering[0].Delivered {
					t.Fatalf("healthy queued input did not recover: %+v", turn)
				}
				if _, err := e.m.Store.ListMeta(); err == nil {
					t.Fatal("strict metadata scan accepted damaged evidence")
				}
			})
		}
	}
}

func TestSessionDeliveryDoesNotWaitForEndingTurn(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(a.ID, 1)
	rec := e.room
	rec.Repo = t.TempDir()
	root, _, err := e.m.StartRoot(rec, "SLEEP:0\nRESPOND: ready", "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(root.ID, 1)
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	ending, err := e.m.beginTurn(a.ID, "SLEEP:0\nRESPOND: ending", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	e.m.Spawner = runner
	meta, err := e.m.Store.ReadMeta(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := e.m.Store.newTurnGate(e.m.RoomStore(), meta, ending.N, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(a.ID, ending.N, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}
	if posts, _, err := gate.Read(0, true); err != nil || len(posts) != 0 {
		t.Fatalf("seal ending turn: %+v %v", posts, err)
	}
	if _, open, err := e.m.Store.gateOpen(a.ID); err != nil || open {
		t.Fatalf("gate remains open: %t %v", open, err)
	}
	post, err := e.m.RoomStore().Post(rec.ID, room.PostInput{Wake: true, From: rec.Name, To: []string{a.Address()}, Text: "SLEEP:0\nRESPOND: after ending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RoomStore().Post(rec.ID, room.PostInput{Wake: true, Human: true, From: rec.Name, To: []string{rec.Name}, Text: "SLEEP:0\nRESPOND: answer owner"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	started := time.Now()
	// A comes first deliberately. It keeps its closed gate and turn lock
	// until after root starts; no sleep or release can hide worker blocking.
	err = e.m.TickSessionTargets(ctx, []string{a.ID, root.ID})
	rootMeta, readErr := e.m.Store.ReadMeta(root.ID)
	t.Logf("delivery pass took %s; root has %d turns", time.Since(started), rootMeta.Turns)
	if err != nil || readErr != nil || rootMeta.Turns != 2 {
		t.Fatalf("ending session blocked idle root: %v %v turns=%d", err, readErr, rootMeta.Turns)
	}
	rootTurn, err := e.m.Store.ReadTurn(root.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("root wake latency: %s", rootTurn.Created.Sub(started))
	if _, _, err := e.waitTurn(ctx, root.ID, 2); err != nil {
		t.Fatal(err)
	}
	meta, err = e.m.Store.ReadMeta(a.ID)
	if err != nil || meta.Turns != ending.N {
		t.Fatalf("ending turn was replaced: %+v %v", meta, err)
	}
	if _, err := e.m.Store.UpdateTurn(a.ID, ending.N, func(turn *Turn) { turn.Status, turn.Ended = TurnOK, time.Now() }); err != nil {
		t.Fatal(err)
	}
	if err := held.lock.Close(); err != nil {
		t.Fatal(err)
	}
	// Its completion event schedules this next pass, which must retain and
	// deliver the post that could not enter the closed gate.
	if err := e.m.TickSessionTargets(e.ctx, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	_, followup := e.wait(a.ID, ending.N+1)
	if len(followup.Steering) != 1 || followup.Steering[0].ID != post.ID || !followup.Steering[0].Delivered {
		t.Fatalf("ending session lost pending post: %+v", followup.Steering)
	}
}

func TestPendingPostUsesTurnEndEventAfterLockRelease(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(a.ID, 1)
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	finalized, release := make(chan struct{}, 1), make(chan struct{})
	var finishes atomic.Int32
	e.m.checkpoint = func(stage string) {
		if stage == "turn-finished" && finishes.Add(1) == 1 {
			finalized <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	type event struct {
		id  string
		ack chan error
	}
	events := make(chan event, 2)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case input := <-events:
				input.ack <- e.m.TickSessionTargets(ctx, []string{input.id})
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	reported := make(chan error, 1)
	e.m.TurnReporter = func(_ context.Context, m *Manager, id string, n int) error {
		if n != 2 {
			return nil
		}
		ack := make(chan error, 1)
		events <- event{id, ack}
		select {
		case err := <-ack:
			reported <- err
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if _, err := e.send(a.ID, "SLEEP:0\nRESPOND: ending", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finalized:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	late, err := e.m.RoomStore().Post(a.RoomID, room.PostInput{Wake: true, From: a.Room, To: []string{a.Address()}, Text: "SLEEP:0\nRESPOND: late"})
	if err != nil {
		t.Fatal(err)
	}
	// The first completion notification is deliberately early: this pass
	// must leave the saved post pending while the ending lock is still held.
	ack := make(chan error, 1)
	events <- event{a.ID, ack}
	select {
	case err := <-ack:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	before, err := e.m.Store.ReadMeta(a.ID)
	if err != nil || before.Turns != 2 {
		t.Fatalf("early event replaced ending turn: %+v %v", before, err)
	}
	close(release)
	// Only the actual turn-end event may drive the next pass. There is no
	// fallback clock and no manual delivery call after release.
	select {
	case err := <-reported:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := e.m.Store.ReadMeta(a.ID)
	if err != nil || after.Turns != 3 {
		t.Fatalf("turn-end event did not deliver pending input: turns=%d %v", after.Turns, err)
	}
	_, followup, err := e.waitTurn(ctx, a.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(followup.Steering) != 1 || followup.Steering[0].ID != late.ID || !followup.Steering[0].Delivered {
		t.Fatalf("late input was not delivered once: %+v", followup.Steering)
	}
}

func TestSessionWorkerNeverWakesTheKeeper(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready", func(o *StartOptions) { o.Service, o.Role, o.Name = true, RoleKeeper, "keeper" })
	e.wait(info.ID, 1)
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room + ":worker", To: []string{info.Address()}, Text: "g1: waiting for CI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if meta, _ := e.m.Store.ReadMeta(info.ID); meta.Turns != 1 {
		t.Fatal("a reply woke the keeper")
	}
	turn, err := e.m.Assign(e.ctx, info.ID, "assignment", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, turn = e.wait(info.ID, turn.N); len(turn.Steering) != 1 {
		t.Fatalf("the next assignment did not read the stored reply: %+v", turn.Steering)
	}
}
