package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
)

func awaitInput[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatal("input event did not arrive")
		var zero T
		return zero
	}
}

func newInputWaitFixture(t *testing.T) (*testEnv, Info, InputClaim, context.Context, *state.Writer) {
	t.Helper()
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	gate := e.m.Store.newTurnGate(e.m.rooms(), info.Meta, 1, info.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := readQueue(gateDir(t, e, info.ID)); err != nil {
		t.Fatal(err)
	}
	e.m.InputEvents = hub.New(1)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	return e, info, InputClaim{Turn: 1, Cursor: info.RoomCursor, Depth: info.Depth, ParentSession: info.ParentSession}, ctx, state.For(db)
}

func gateDir(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunnerWaitInputChecksBeforeWaitingAndNeverClaims(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "already ready", true: "subscribe before check"}[race], func(t *testing.T) {
			e, info, input, ctx, w := newInputWaitFixture(t)
			var posted string
			var postWrites int
			post := func() {
				start := w.Health().Writes
				posted = postClaimInput(t, e, info, "ready").ID
				postWrites += w.Health().Writes - start
				e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: info.RoomID})
			}
			if race {
				once := false
				e.m.checkpoint = func(stage string) {
					if stage == "input-wait-idle" && !once {
						once = true
						post()
					}
				}
			} else {
				post()
				postWrites = 0
			}
			before := w.Health().Writes
			ready, err := e.m.RunnerWaitInput(ctx, input)
			if err != nil || !ready {
				t.Fatalf("wait readiness = %v, %v", ready, err)
			}
			if delta := w.Health().Writes - before; delta != postWrites {
				t.Fatalf("wait wrote %d times beyond %d fixture post writes", delta, postWrites)
			}
			queue, err := readCanonicalQueue(gateDir(t, e, info.ID))
			if err != nil || queue.Claimed[posted] != 0 {
				t.Fatalf("wait claimed input: %+v, %v", queue, err)
			}
			if e.m.InputEvents.Stats().Streams != 0 {
				t.Fatal("completed wait kept its subscription")
			}
		})
	}
}

func TestRunnerWaitInputMissingQueueIgnoresRetainedFile(t *testing.T) {
	e, info, input, ctx, w := newInputWaitFixture(t)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	msg := postClaimInput(t, e, info, "still available")
	if _, err := db.Exec("DELETE FROM session_queues WHERE session=?", info.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(gateDir(t, e, info.ID), queueFile)
	stale := []byte(`{"claimed":{"` + msg.ID + `":1},"cancelled":{}}`)
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	before := w.Health().Writes
	ready, err := e.m.RunnerWaitInput(ctx, input)
	if err != nil || !ready {
		t.Fatalf("absent queue wait = %v, %v", ready, err)
	}
	if delta := w.Health().Writes - before; delta != 0 {
		t.Fatalf("missing queue caused %d writes", delta)
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_queues WHERE session=?", info.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("queue row was created: %d, %v", rows, err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, stale) {
		t.Fatalf("retained file changed: %s, %v", raw, err)
	}
}

func TestRunnerWaitInputTimeoutAndCancelReleaseSubscription(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelWait], func(t *testing.T) {
			e, _, input, actor, w := newInputWaitFixture(t)
			ctx, cancel := context.WithCancel(actor)
			defer cancel()
			timeout := make(chan time.Time, 1)
			e.m.inputWaitDeadline = func() (<-chan time.Time, func()) { return timeout, func() {} }
			idle := make(chan struct{}, 1)
			e.m.checkpoint = func(stage string) {
				if stage == "input-wait-idle" {
					idle <- struct{}{}
				}
			}
			before := w.Health().Writes
			done := make(chan error, 1)
			go func() {
				ready, err := e.m.RunnerWaitInput(ctx, input)
				if ready {
					err = errors.New("idle wait returned ready")
				}
				done <- err
			}()
			awaitInput(t, idle)
			if cancelWait {
				cancel()
			} else {
				timeout <- time.Now()
			}
			err := awaitInput(t, done)
			if cancelWait && !errors.Is(err, context.Canceled) || !cancelWait && err != nil {
				t.Fatalf("wait ended: %v", err)
			}
			if delta := w.Health().Writes - before; delta != 0 {
				t.Fatalf("idle wait wrote %d times", delta)
			}
			if e.m.InputEvents.Stats().Streams != 0 {
				t.Fatal("wait kept its subscription")
			}
		})
	}
}

func TestRunnerWaitInputOverflowRechecksWithoutFalseReadiness(t *testing.T) {
	e, info, input, ctx, _ := newInputWaitFixture(t)
	checks := 0
	e.m.checkpoint = func(stage string) {
		if stage != "input-wait-check" {
			return
		}
		checks++
		if checks == 1 {
			for range hub.Buffer + 1 {
				e.m.InputEvents.Publish(hub.Event{Topic: "unrelated", Room: "other"})
			}
		} else if checks == 2 {
			postClaimInput(t, e, info, "after overflow")
		}
	}
	ready, err := e.m.RunnerWaitInput(ctx, input)
	if err != nil || !ready || checks != 2 {
		t.Fatalf("overflow readiness %v, checks %d, error %v", ready, checks, err)
	}
	// Only the test's post may write; the wait must not claim it.
	queue, err := readCanonicalQueue(gateDir(t, e, info.ID))
	if err != nil || len(queue.Claimed) != 0 {
		t.Fatalf("wait changed queue %+v, %v", queue, err)
	}
	if e.m.InputEvents.Stats().Dropped != 1 {
		t.Fatal("test did not overflow its subscription")
	}
}

func TestRunnerWaitInputRefusesInvalidCallerOrTurn(t *testing.T) {
	e, info, input, actor, w := newInputWaitFixture(t)
	for _, ctx := range []context.Context{t.Context(), authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: NewID()}), authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), callspec.WithRequest(actor, info.ID+"/not-a-wait")} {
		if _, err := e.m.RunnerWaitInput(ctx, input); err == nil {
			t.Fatal("invalid caller or request ID waited")
		}
	}
	input.Turn = 2
	if _, err := e.m.RunnerWaitInput(actor, input); err == nil {
		t.Fatal("another turn waited")
	}
	input.Turn = 1
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_turns WHERE session=?", info.ID); err != nil {
		t.Fatal(err)
	}
	before := w.Health().Writes
	if _, err := e.m.RunnerWaitInput(actor, input); err == nil {
		t.Fatal("missing canonical turn waited")
	}
	if delta := w.Health().Writes - before; delta != 0 {
		t.Fatalf("missing turn caused %d writes", delta)
	}
}

func TestRunnerWaitInputFiltersUnrelatedEvents(t *testing.T) {
	e, info, input, ctx, _ := newInputWaitFixture(t)
	checks := 0
	idle := make(chan int, 2)
	e.m.checkpoint = func(stage string) {
		if stage == "input-wait-check" {
			checks++
		}
		if stage == "input-wait-idle" {
			idle <- checks
		}
	}
	done := make(chan error, 1)
	go func() {
		ready, err := e.m.RunnerWaitInput(ctx, input)
		if !ready && err == nil {
			err = errors.New("wait returned false readiness")
		}
		done <- err
	}()
	if n := awaitInput(t, idle); n != 1 {
		t.Fatalf("initial checks = %d", n)
	}
	e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: "other"})
	e.m.InputEvents.Publish(hub.Event{Topic: "unrelated", Room: info.RoomID})
	e.m.InputEvents.Publish(hub.Event{Topic: "sessions", Key: "another session"})
	e.m.InputEvents.Publish(hub.Event{Topic: "members", Room: info.RoomID})
	if n := awaitInput(t, idle); n != 2 {
		t.Fatalf("unrelated events caused extra checks: %d", n)
	}
	postClaimInput(t, e, info, "wake for this room")
	e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: info.RoomID})
	if err := awaitInput(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerIdleMinuteUsesWaitsAndNoClaims(t *testing.T) {
	f := newClaimFixture(t, "already handled at startup")
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.info.ID})
	if _, err := f.e.m.RunnerClaimInput(actor, InputClaim{Turn: 1, Startup: true}); err != nil {
		t.Fatal(err)
	}
	deadlines := make(chan chan time.Time, 4)
	f.e.m.inputWaitDeadline = func() (<-chan time.Time, func()) {
		timeout := make(chan time.Time, 1)
		deadlines <- timeout
		return timeout, func() {}
	}
	idle := make(chan struct{}, 4)
	f.e.m.checkpoint = func(stage string) {
		if stage == "input-wait-idle" {
			idle <- struct{}{}
		}
	}
	eng := f.serve(t)
	gate := f.openGate(t, newLiveClock())
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
	before := eng.w.Health().Writes
	wake := gate.Wake()
	first := awaitInput(t, deadlines)
	awaitInput(t, idle)
	select {
	case <-wake:
		t.Fatal("idle wait announced readiness")
	default:
	}
	// Advance one 30-second wait. The next half-minute starts another read,
	// with no empty claim and no mutation request ID.
	virtual := time.Unix(1_800_000_000, 0)
	first <- virtual.Add(inputWait)
	second := awaitInput(t, deadlines)
	awaitInput(t, idle)
	second <- virtual.Add(2 * inputWait)
	awaitInput(t, deadlines)
	awaitInput(t, idle)
	eng.mu.Lock()
	waits, claims := eng.waitCalls, eng.claims
	eng.mu.Unlock()
	if waits != 3 || claims != 0 {
		t.Fatalf("virtual idle minute: %d waits, %d claims", waits, claims)
	}
	if delta := eng.w.Health().Writes - before; delta != 0 {
		t.Fatalf("virtual idle minute wrote %d times", delta)
	}
	var receipts int
	db, err := database.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("idle receipts = %d, %v", receipts, err)
	}
	// A real post wakes this same doorbell and enters the original mutation
	// outbox exactly once. Its receipt remains available after delivery.
	message := postClaimInput(t, f.e, f.info, "new input")
	f.e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: f.info.RoomID})
	awaitInput(t, wake)
	got := awaitInput(t, readGate(gate, false))
	if got.err != nil || countSteer(got.msgs, message.ID) != 1 {
		t.Fatalf("woken input = %+v, %v", got.msgs, got.err)
	}
	eng.mu.Lock()
	claims = eng.claims
	eng.mu.Unlock()
	if claims != 1 {
		t.Fatalf("wake made %d claims", claims)
	}
	t.Logf("virtual idle minute: two completed 30-second waits and one boundary wait, 0 claims, 0 SQL writes, 0 receipts; one real post then made one claim")
}

func TestFinalReadJoinsWaitAndAllowsEarlierNotes(t *testing.T) {
	f := newClaimFixture(t, "startup input")
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.info.ID})
	if _, err := f.e.m.RunnerClaimInput(actor, InputClaim{Turn: 1, Startup: true}); err != nil {
		t.Fatal(err)
	}
	idle, ended := make(chan struct{}, 1), make(chan struct{}, 1)
	f.e.m.checkpoint = func(stage string) {
		if stage == "input-wait-idle" {
			idle <- struct{}{}
		}
		if stage == "input-wait-end" {
			ended <- struct{}{}
		}
	}
	eng := f.serve(t)
	gate := f.openGate(t, newLiveClock())
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
	wake := gate.Wake()
	awaitInput(t, idle)
	if err := gate.writes.noteWait(t.Context(), TurnNote{HarnessSession: "note-during-idle-wait"}); err != nil {
		t.Fatal(err)
	}
	turn, err := f.e.m.Store.ReadTurn(f.info.ID, 1)
	if err != nil || turn.HarnessSession != "note-during-idle-wait" {
		t.Fatalf("note blocked behind input wait: %+v, %v", turn, err)
	}
	select {
	case <-wake:
		t.Fatal("unrelated note announced input readiness")
	default:
	}
	result := awaitInput(t, readGate(gate, true))
	awaitInput(t, ended)
	if result.err != nil || len(result.msgs) != 0 {
		t.Fatalf("final read = %+v", result)
	}
	if gate.waiting != nil || gate.pending != nil {
		t.Fatal("final read left an input operation alive")
	}
	if state := readGateState(f.dir); state.Active || state.ClosedAt != result.cursor {
		t.Fatalf("final read failed to seal: %+v", state)
	}
	eng.mu.Lock()
	waits, claims := eng.waitCalls, eng.claims
	eng.mu.Unlock()
	if waits != 1 || claims != 1 {
		t.Fatalf("final read made %d waits and %d claims", waits, claims)
	}
}

func TestRunnerWaitInputAbsentQueueCanStayIdle(t *testing.T) {
	e, info, input, ctx, w := newInputWaitFixture(t)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_queues WHERE session=?", info.ID); err != nil {
		t.Fatal(err)
	}
	stale := []byte(`{"claimed":{"not-a-room-post":7},"cancelled":{}}`)
	path := filepath.Join(gateDir(t, e, info.ID), queueFile)
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()
	e.m.inputWaitDeadline = func() (<-chan time.Time, func()) { return timeout, func() {} }
	before := w.Health().Writes
	ready, err := e.m.RunnerWaitInput(ctx, input)
	if err != nil || ready || w.Health().Writes != before {
		t.Fatalf("absent idle queue: ready %v, writes %d, error %v", ready, w.Health().Writes-before, err)
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_queues WHERE session=?", info.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("idle read created queue row: %d, %v", rows, err)
	}
}

func TestLiveInputWaitRetriesTransientOutages(t *testing.T) {
	for _, fault := range []int{-1, 503} {
		t.Run(fmt.Sprint(fault), func(t *testing.T) {
			f := newClaimFixture(t, "still queued")
			eng := f.serve(t)
			eng.waitFault = fault
			clock := newLiveClock()
			gate := f.openGate(t, clock)
			t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
			retry := make(chan time.Time, 1)
			backoff := make(chan time.Duration, 1)
			gate.writes.waitRetryAfter = func(delay time.Duration) <-chan time.Time { backoff <- delay; return retry }
			pid := readGateState(f.dir).Pid
			reading := readGate(gate, false)
			timer := time.NewTimer(30 * time.Second)
			select {
			case delay := <-backoff:
				if delay != 100*time.Millisecond {
					t.Fatalf("first read retry delay=%v", delay)
				}
			case result := <-reading:
				timer.Stop()
				t.Fatalf("transient outage ended live input: %+v", result)
			case <-timer.C:
				t.Fatal("wait did not reach its retry gate")
			}
			timer.Stop()
			eng.mu.Lock()
			claims := eng.claims
			eng.mu.Unlock()
			if claims != 0 || readGateState(f.dir).Pid != pid {
				t.Fatal("read outage changed the active runner or made a claim")
			}
			retry <- time.Now()
			result := awaitInput(t, reading)
			if result.err != nil || countSteer(result.msgs, f.msg.ID) != 1 {
				t.Fatalf("recovered read = %+v", result)
			}
			if readGateState(f.dir).Pid != pid {
				t.Fatal("recovery replaced the runner")
			}
			eng.mu.Lock()
			waits, claims := eng.waitCalls, eng.claims
			eng.mu.Unlock()
			if waits != 2 || claims != 1 {
				t.Fatalf("recovery made %d waits and %d claims", waits, claims)
			}
		})
	}
}

func TestRunnerWaitInputNeverImportsInheritedState(t *testing.T) {
	for _, missing := range []string{"origin record", "origin turn", "target historical turn"} {
		t.Run(missing, func(t *testing.T) {
			e, target, input, ctx, w := newInputWaitFixture(t)
			origin := completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateMeta(target.ID, func(meta *Meta) {
				meta.InheritedInboxes = []InheritedInbox{{Session: origin.ID, Address: origin.Address()}}
			}); err != nil {
				t.Fatal(err)
			}
			postClaimInput(t, e, origin, "inherited input")
			db, err := database.Open(filepath.Dir(e.m.Store.root()))
			if err != nil {
				t.Fatal(err)
			}
			if err := state.MigrateRequests(filepath.Dir(e.m.Store.root())); err != nil {
				t.Fatal(err)
			}
			deletedID := origin.ID
			deletedTable := "session_turns"
			deletedTurn := 1
			if missing == "origin record" {
				deletedTable = "session_records"
				raw, err := json.Marshal(origin.Meta)
				if err != nil {
					t.Fatal(err)
				}
				recordtest.WriteRaw(t, e.m.Store.root(), origin.ID, raw)
			} else {
				if missing == "target historical turn" {
					deletedID = target.ID
					if err := e.m.Store.writeTurn(target.ID, Turn{N: 2, Status: TurnRunning}); err != nil {
						t.Fatal(err)
					}
					if _, err := e.m.Store.UpdateMeta(target.ID, func(meta *Meta) { meta.Turns = 2 }); err != nil {
						t.Fatal(err)
					}
					input.Turn = 2
					if err := writeGateState(gateDir(t, e, target.ID), gateState{Active: true, Pid: os.Getpid(), Turn: 2}); err != nil {
						t.Fatal(err)
					}
				}
				turn, err := e.m.Store.ReadTurn(deletedID, deletedTurn)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(turn)
				if err != nil {
					t.Fatal(err)
				}
				path, err := e.m.Store.turnPath(deletedID, deletedTurn)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("DELETE FROM session_turns WHERE session=? AND n=?", deletedID, deletedTurn); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("DELETE FROM state_mirrors WHERE key=?", sessionlife.TurnMirrorKey(deletedID, deletedTurn)); err != nil {
					t.Fatal(err)
				}
			}
			count := func(query string, args ...any) int {
				t.Helper()
				var n int
				if err := db.QueryRow(query, args...).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			stamps := count("SELECT COUNT(*) FROM state_mirrors")
			timeout := make(chan time.Time, 1)
			timeout <- time.Now()
			e.m.inputWaitDeadline = func() (<-chan time.Time, func()) { return timeout, func() {} }
			before := w.Health().Writes
			ready, waitErr := e.m.RunnerWaitInput(ctx, input)
			var rows int
			if deletedTable == "session_records" {
				rows = count("SELECT COUNT(*) FROM session_records WHERE id=?", deletedID)
			} else {
				rows = count("SELECT COUNT(*) FROM session_turns WHERE session=? AND n=?", deletedID, deletedTurn)
			}
			if waitErr == nil || ready || w.Health().Writes != before || rows != 0 || count("SELECT COUNT(*) FROM state_mirrors") != stamps || count("SELECT COUNT(*) FROM request_ids") != 0 {
				t.Fatalf("inherited wait: ready %v, error %v, writes %d, imported rows %d", ready, waitErr, w.Health().Writes-before, rows)
			}
		})
	}
}

func TestInputWaitRetryKeepsRefusalAndFinalCancellation(t *testing.T) {
	t.Run("final refusal", func(t *testing.T) {
		f := newClaimFixture(t, "input")
		eng := f.serve(t)
		eng.waitFault = 403
		gate := f.openGate(t, newLiveClock())
		t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
		retried := make(chan time.Duration, 1)
		gate.writes.waitRetryAfter = func(d time.Duration) <-chan time.Time { retried <- d; return make(chan time.Time) }
		result := awaitInput(t, readGate(gate, false))
		var remote *engineclient.RemoteError
		if !errors.As(result.err, &remote) || remote.Status != 403 {
			t.Fatalf("final refusal = %v", result.err)
		}
		select {
		case <-retried:
			t.Fatal("final refusal was retried")
		default:
		}
	})
	t.Run("cancel during backoff", func(t *testing.T) {
		f := newClaimFixture(t, "startup")
		actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.info.ID})
		if _, err := f.e.m.RunnerClaimInput(actor, InputClaim{Turn: 1, Startup: true}); err != nil {
			t.Fatal(err)
		}
		eng := f.serve(t)
		eng.waitFault = 503
		gate := f.openGate(t, newLiveClock())
		t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
		retrying := make(chan time.Duration, 1)
		gate.writes.waitRetryAfter = func(d time.Duration) <-chan time.Time { retrying <- d; return make(chan time.Time) }
		gate.Wake()
		awaitInput(t, retrying)
		result := awaitInput(t, readGate(gate, true))
		if result.err != nil || len(result.msgs) != 0 || gate.waiting != nil || readGateState(f.dir).Active {
			t.Fatalf("final read did not join retry and seal: %+v", result)
		}
		eng.mu.Lock()
		waits := eng.waitCalls
		eng.mu.Unlock()
		if waits != 1 {
			t.Fatalf("cancellation started %d wait requests", waits)
		}
	})
}

// appendUnrelatedRoomPages bypasses per-post setup work to make a real log
// larger than a ReadFrom page, using valid, unrelated room messages.
func appendUnrelatedRoomPages(t *testing.T, e *testEnv, info Info) {
	t.Helper()
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	pad := strings.Repeat("x", room.MaxMessageBytes)
	for i := range 256 {
		if err := encoder.Encode(room.Message{ID: fmt.Sprintf("msg_%016x", i), RoomID: info.RoomID,
			From: info.Room, To: []string{info.Room + ":other"}, Text: pad}); err != nil {
			t.Fatal(err)
		}
	}
	if log.Len() <= 4<<20 {
		t.Fatal("fixture does not span room pages")
	}
	path := filepath.Join(e.m.rooms().Root, info.RoomID, "messages.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(log.Bytes())
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append room pages: %v, %v", writeErr, closeErr)
	}
}

func TestRunnerWaitInputReadsPastRoomPagesWithoutWriting(t *testing.T) {
	e, info, input, ctx, w := newInputWaitFixture(t)
	appendUnrelatedRoomPages(t, e, info)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateRequests(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	// A ready timeout makes the old first-page-only wait fail immediately.
	e.m.inputWaitDeadline = func() (<-chan time.Time, func()) {
		timeout := make(chan time.Time, 1)
		timeout <- time.Now()
		return timeout, func() {}
	}
	assertReadOnly := func() {
		t.Helper()
		var receipts int
		if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 {
			t.Fatalf("read-only page scan receipts=%d: %v", receipts, err)
		}
	}
	before := w.Health().Writes
	ready, err := e.m.RunnerWaitInput(ctx, input)
	if err != nil || ready || w.Health().Writes != before {
		t.Fatalf("unrelated pages: ready=%v, err=%v, writes=%d", ready, err, w.Health().Writes-before)
	}
	assertReadOnly()
	msg := postClaimInput(t, e, info, "input after unrelated pages")
	page, next, err := e.m.rooms().ReadFrom(info.RoomID, input.Cursor)
	if err != nil || next <= input.Cursor {
		t.Fatalf("first page cursor=%d: %v", next, err)
	}
	for _, post := range page {
		if post.ID == msg.ID {
			t.Fatal("addressed input must be after the first page")
		}
	}
	before = w.Health().Writes
	ready, err = e.m.RunnerWaitInput(ctx, input)
	if err != nil || !ready || w.Health().Writes != before {
		t.Fatalf("later input: ready=%v, err=%v, writes=%d", ready, err, w.Health().Writes-before)
	}
	queue, err := readCanonicalQueue(gateDir(t, e, info.ID))
	if err != nil || queue.Claimed[msg.ID] != 0 {
		t.Fatalf("readiness claimed later input: %+v, %v", queue, err)
	}
	assertReadOnly()
}

func TestLiveInputDoorbellDeliversPastRoomPages(t *testing.T) {
	f := newClaimFixture(t, "startup input")
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.info.ID})
	if _, err := f.e.m.RunnerClaimInput(actor, InputClaim{Turn: 1, Startup: true}); err != nil {
		t.Fatal(err)
	}
	appendUnrelatedRoomPages(t, f.e, f.info)
	idle := make(chan struct{}, 4)
	f.e.m.checkpoint = func(stage string) {
		if stage == "input-wait-idle" {
			idle <- struct{}{}
		}
	}
	eng := f.serve(t)
	gate := f.openGate(t, newLiveClock())
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(func() {}, []*engineclient.Outbox{gate.writes.out}, eng) })
	before := eng.w.Health().Writes
	wake := gate.Wake()
	awaitInput(t, idle)
	eng.mu.Lock()
	claims := eng.claims
	eng.mu.Unlock()
	if claims != 0 || eng.w.Health().Writes != before {
		t.Fatalf("idle pages made %d claims and %d writes", claims, eng.w.Health().Writes-before)
	}
	db, err := database.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("idle pages saved %d receipts: %v", receipts, err)
	}
	message := postClaimInput(t, f.e, f.info, "deliver after the first page")
	f.e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: f.info.RoomID})
	// Claims keep their original page-sized cursor semantics. Readiness can
	// now reach the later post and wake each authoritative page read.
	delivered := 0
	var lastInput InputClaim
	for range 3 {
		awaitInput(t, wake)
		lastInput = InputClaim{Turn: gate.turn, Cursor: gate.cursor, Depth: gate.meta.Depth, ParentSession: gate.meta.ParentSession}
		before = eng.w.Health().Writes
		result := awaitInput(t, readGate(gate, false))
		if result.err != nil {
			t.Fatal(result.err)
		}
		delivered += countSteer(result.msgs, message.ID)
		if delivered != 0 {
			break
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 0 || eng.w.Health().Writes != before {
			t.Fatalf("empty page saved %d receipts and made %d writes: %v", receipts, eng.w.Health().Writes-before, err)
		}
		wake = gate.Wake()
	}
	queue, err := readCanonicalQueue(f.dir)
	if err != nil || delivered != 1 || queue.Claimed[message.ID] != 1 {
		t.Fatalf("later delivery=%d, claim=%d: %v", delivered, queue.Claimed[message.ID], err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("input receipt count=%d: %v", receipts, err)
	}
	calls, claims, _, _, _, _, _ := eng.snapshot()
	key := "runner/" + calls[len(calls)-1]
	saved, done, err := eng.w.RequestDone(t.Context(), key)
	if err != nil || !done {
		t.Fatalf("later input receipt done=%v: %v", done, err)
	}
	var want, replay ClaimedInput
	if err := json.Unmarshal(saved, &want); err != nil {
		t.Fatal(err)
	}
	before = eng.w.Health().Writes
	if err := eng.client.Call(callspec.WithRequest(t.Context(), calls[len(calls)-1]), "RunnerClaimInput", []any{lastInput}, &replay); err != nil {
		t.Fatal(err)
	}
	_, afterClaims, replayed, _, _, _, _ := eng.snapshot()
	if !sameClaim(want, replay) || len(replay.Posts) != 1 || replay.Posts[0].Steer.ID != message.ID || afterClaims != claims || replayed != 1 || eng.w.Health().Writes != before {
		t.Fatalf("later input replay=%+v, claims=%d, replays=%d, writes=%d", replay, afterClaims, replayed, eng.w.Health().Writes-before)
	}
}

// countedRecords distinguishes revision checks from full record loads.
type countedRecords struct {
	state.Reader
	loads int
}

func (r *countedRecords) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.Contains(query, "SELECT meta") {
		r.loads++
	}
	return r.Reader.QueryRowContext(ctx, query, args...)
}

func TestWaitRecordCacheTracksRevision(t *testing.T) {
	e, info, _, ctx, _ := newInputWaitFixture(t)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedRecords{Reader: db}
	reader := canonicalInputReader{Store: e.m.Store, ctx: ctx, db: counted}
	for range 3 {
		if _, err := reader.ReadMeta(info.ID); err != nil {
			t.Fatal(err)
		}
	}
	if counted.loads != 1 {
		t.Fatalf("unchanged record loaded %d times, want 1", counted.loads)
	}
	meta := info.Meta
	meta.Name = "renamed"
	recordtest.Write(t, e.m.Store.root(), info.ID, meta)
	for range 2 {
		got, err := reader.ReadMeta(info.ID)
		if err != nil || got.Name != meta.Name {
			t.Fatalf("updated record: name %q, error %v", got.Name, err)
		}
	}
	if counted.loads != 2 {
		t.Fatalf("updated record loaded %d times, want 2 total", counted.loads)
	}
}
