package subagent

import (
	"context"
	"dyna-agent/internal/goals"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/shells"
)

func roomControlEnv(t *testing.T) *testEnv {
	e := newTestEnv(t)
	e.joinRunners()
	e.m.ResumeRoomLaunch = func(m *Manager, id string, r uint64) error { return m.runRoomResume(e.ctx, id, r) }
	return e
}
func TestRoomPauseCoalescesQueueWithNativeIdentity(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e)
	before, _ := e.m.Store.ReadMeta(info.ID)
	p, err := e.m.PauseRoomSessions(e.ctx, info.RoomID)
	if err != nil || !p.State.Paused {
		t.Fatalf("pause: %+v %v", p, err)
	}
	for i := 0; i < 3; i++ {
		result, err := e.send(info.ID, "SLEEP:1 RESPOND: held", WakeOptions{})
		if err != nil || !result.Paused {
			t.Fatalf("wake: %+v %v", result, err)
		}
	}
	after, _ := e.m.Store.ReadMeta(info.ID)
	if after.Turns != 1 || !before.LastActivity.Equal(after.LastActivity) {
		t.Fatal("pause changed turn or activity")
	}
	state, err := e.m.RoomSessionControls(info.RoomID)
	if err != nil || state.Held != 1 {
		t.Fatalf("state: %+v %v", state, err)
	}
	if _, err = e.m.ResumeRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatal(err)
	}
	_, done := e.wait(info.ID, 2)
	if done.HarnessSession != before.HarnessSession || len(done.Steering) != 3 {
		t.Fatalf("identity/queue: %+v", done)
	}
	if _, err = e.m.ResumeRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatal(err)
	}
	final, _ := e.m.Store.ReadMeta(info.ID)
	if final.Turns != 2 {
		t.Fatal("repeat resume duplicated work")
	}
}
func TestRoomPauseKeepsActiveCallAndHoldsNewStart(t *testing.T) {
	e := roomControlEnv(t)
	ctx, cancel := context.WithTimeout(e.ctx, 9*time.Second)
	defer cancel()
	accepted := make(chan struct{}, 1)
	releaseActive := make(chan struct{})
	held := make(chan struct{}, 1)
	releaseHeld := make(chan struct{})
	var starts atomic.Int32
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" && starts.Add(1) == 1 {
			accepted <- struct{}{}
			select {
			case <-releaseActive:
			case <-ctx.Done():
			}
		}
	}
	e.m.TransferSleep = func(context.Context, time.Duration) error {
		select {
		case held <- struct{}{}:
		default:
		}
		select {
		case <-releaseHeld:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	waitEvent := func(event <-chan struct{}) {
		t.Helper()
		select {
		case <-event:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	waitTurn := func(id string) Turn {
		t.Helper()
		_, turn, err := e.waitTurn(ctx, id, 1)
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	active, _ := e.start("SLEEP:0\nRESPOND: active")
	waitEvent(accepted)
	turn, err := e.m.Store.ReadTurn(active.ID, 1)
	if err != nil || !turn.HeldAccepted {
		t.Fatalf("provider did not accept active input: %+v %v", turn, err)
	}
	if _, err := e.m.PauseRoomSessions(ctx, active.RoomID); err != nil {
		t.Fatal(err)
	}
	close(releaseActive)
	if done := waitTurn(active.ID); done.Status != TurnOK {
		t.Fatal("pause stopped active call")
	}
	queued, _ := e.start("SLEEP:0\nRESPOND: queued")
	waitEvent(held)
	turn, err = e.m.Store.ReadTurn(queued.ID, 1)
	if err != nil || !turn.RoomHeld || turn.Done() || turn.HarnessSession != "" {
		t.Fatalf("new runner was not held before the harness: %+v %v", turn, err)
	}
	if _, err := e.m.ResumeRoomSessions(ctx, queued.RoomID); err != nil {
		t.Fatal(err)
	}
	close(releaseHeld)
	if done := waitTurn(queued.ID); done.Status != TurnOK {
		t.Fatalf("resume: %+v", done)
	}
}
func TestRoomResumeCrashReplayAndStaleGeneration(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: one")
	e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: two")
	e.wait(b.ID, 1)
	e.m.PauseRoomSessions(e.ctx, a.RoomID)
	e.send(a.ID, "SLEEP:1 RESPOND: pending one", WakeOptions{})
	e.send(b.ID, "SLEEP:1 RESPOND: pending two", WakeOptions{})
	e.m.ResumeRoomLaunch = func(*Manager, string, uint64) error { return errors.New("simulated launch failure") }
	if _, err := e.m.ResumeRoomSessions(e.ctx, a.RoomID); err == nil {
		t.Fatal("expected failed worker launch")
	}
	p, _ := e.m.rooms().SessionPause(a.RoomID)
	if !p.ResumePending || p.Paused {
		t.Fatalf("recovery not durable: %+v", p)
	}
	e.m.PauseRoomSessions(e.ctx, a.RoomID)
	if err := e.m.runRoomResume(e.ctx, a.RoomID, p.Revision); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		meta, _ := e.m.Store.ReadMeta(id)
		if meta.Turns != 1 {
			t.Fatal("stale resume admitted a turn")
		}
	}
	e.m.ResumeRoomLaunch = func(m *Manager, id string, r uint64) error { return m.runRoomResume(e.ctx, id, r) }
	if _, err := e.m.ResumeRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	e.wait(a.ID, 2)
	e.wait(b.ID, 2)
}
func TestRoomClearRechecksPendingProtectionAndPreservesFiles(t *testing.T) {
	for _, protect := range []string{"none", "post-before-wake", "claim", "child", "pr", "goal", "transfer", "held"} {
		t.Run(protect, func(t *testing.T) {
			e := roomControlEnv(t)
			a, _ := e.start("SLEEP:1 RESPOND: old")
			a, _ = e.wait(a.ID, 1)
			e.doneGoals()
			old := time.Now().Add(-31 * time.Minute)
			e.m.Store.UpdateMeta(a.ID, func(m *Meta) { m.LastActivity, m.Finished = old, old; m.Status = StatusDormant })
			switch protect {
			case "post-before-wake":
				e.m.rooms().Post(a.RoomID, room.PostInput{Wake: true, From: a.Room, To: []string{a.Address()}, Text: "unread"})
			case "claim":
				msg, err := e.m.rooms().Post(a.RoomID, room.PostInput{Wake: true, From: a.Room, To: []string{a.Address()}, Text: "unacknowledged claim"})
				if err != nil {
					t.Fatal(err)
				}
				claimPostsFor(t, e, a.ID, 1, msg.ID)
			case "child":
				// A child delegates from the parent's goal, so the parent holds one.
				if _, err := e.m.goalStore().Add(a.RoomID, "owner", goals.Input{Text: "protecting", Holders: []string{a.ID}}); err != nil {
					t.Fatal(err)
				}
				stampParent(t, a)
				child, _ := e.start("SLEEP:1 RESPOND: child")
				e.wait(child.ID, 1)
			case "pr":
				writePRLink(t, a, "author", "open", true)
			case "goal":
				e.m.goalStore().Add(a.RoomID, "owner", goals.Input{Text: "unfinished", Holders: []string{a.ID}})
			case "transfer":
				e.m.Store.UpdateMeta(a.ID, func(m *Meta) { m.Transfer = &TransferState{ID: "reserved", Phase: "starting"} })
			case "held":
				e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) { t.RoomHeld = true; t.Status = TurnQueued })
			}
			result, err := closeIdle(e.m, e.ctx, a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			meta, _ := e.m.Store.ReadMeta(a.ID)
			closed := !meta.Closed.IsZero()
			if closed != (protect == "none") {
				t.Fatalf("unsafe close=%t: %+v", closed, result)
			}
			if _, err = os.Stat(filepath.Join(a.Dir)); err != nil {
				t.Fatal("task directory lost", err)
			}
			if _, err = e.m.Store.ReadTurn(a.ID, 1); err != nil {
				t.Fatal("history lost", err)
			}
		})
	}
}
func TestRoomHeldPromptSurvivesReleaseBeforeLaunchAndRepair(t *testing.T) {
	for _, prompt := range []string{"schema correction: preserve exact JSON", "native continuation: continue task"} {
		t.Run(prompt, func(t *testing.T) {
			e := roomControlEnv(t)
			a, _ := e.start("SLEEP:1 RESPOND: initial")
			a, _ = e.wait(a.ID, 1)
			e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) { t.Status = TurnRunning; t.Ended = time.Time{}; t.RoomHeld = true; t.HeldPrompt = prompt })
			if err := e.m.runnerWrites(a.ID, 1).waitSession(e.ctx, prompt); err != nil {
				t.Fatal(err)
			}
			freshStore := &Store{Root: e.m.Store.Root}
			freshStore.Info(a.ID)
			pending, _ := freshStore.ReadTurn(a.ID, 1)
			if pending.Done() || pending.HeldPrompt != prompt || !pending.RoomHeld {
				t.Fatalf("lost pending attempt: %+v", pending)
			}
			if repaired, err := freshStore.RepairTurn(a.ID, 1, "crash"); err != nil || repaired {
				t.Fatal("repair consumed recoverable attempt", err)
			}
			var starts atomic.Int32
			e.m.Spawner = recordRoomSpawn{start: func(_ *Manager, id string, n int, lock *os.File) error {
				defer lock.Close()
				starts.Add(1)
				turn, _ := freshStore.ReadTurn(id, n)
				if turn.HeldPrompt != prompt {
					t.Error("changed exact prompt")
				}
				turn.Status, turn.RoomHeld, turn.HeldPrompt = TurnOK, false, ""
				freshStore.writeTurn(id, turn)
				return nil
			}}
			p, _ := e.m.rooms().SessionPause(a.RoomID)
			if err := e.m.runRoomResume(context.Background(), a.RoomID, p.Revision); err != nil {
				t.Fatal(err)
			}
			e.m.runRoomResume(context.Background(), a.RoomID, p.Revision)
			if starts.Load() != 1 {
				t.Fatal("duplicate recovered turn", starts.Load())
			}
		})
	}
}

type recordRoomSpawn struct {
	start func(*Manager, string, int, *os.File) error
}

func (s recordRoomSpawn) Spawn(m *Manager, id string, n int, f *os.File) error {
	return s.start(m, id, n, f)
}

func TestRoomPauseTransferRejectAndSavedBudget(t *testing.T) {
	e, now := transferEnv(t)
	e.joinRunners()
	var ledgerClock atomic.Int64
	ledgerClock.Store(now.UnixNano())
	e.m.Rooms = e.m.rooms()
	e.m.Rooms.Now = func() time.Time { return time.Unix(0, ledgerClock.Load()) }
	a, _ := e.start("SLEEP:1 RESPOND: work")
	e.wait(a.ID, 1)
	state, err := e.m.TransferContext(context.Background(), a.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	e.m.rooms().SetSessionsPaused(e.ctx, a.RoomID, true)
	if _, err = e.m.TransferContext(context.Background(), a.ID, TransferOptions{Profile: "next", WaitSeconds: 30}); !errors.Is(err, errRoomPaused) {
		t.Fatal("transfer bypassed pause", err)
	}
	*now = now.Add(10 * time.Minute)
	ledgerClock.Store(now.UnixNano())
	e.m.rooms().SetSessionsPaused(e.ctx, a.RoomID, false)
	e.m.advanceTransfer(e.ctx, a.ID, state.ID)
	meta, _ := e.m.Store.ReadMeta(a.ID)
	if meta.Transfer.PausedFor != 10*time.Minute || !meta.Transfer.Deadline.Equal(state.Deadline.Add(10*time.Minute)) {
		t.Fatalf("deadline burned while paused: %+v", meta.Transfer)
	}
	e.m.advanceTransfer(e.ctx, a.ID, state.ID)
	meta, _ = e.m.Store.ReadMeta(a.ID)
	if meta.Transfer.PausedFor != 10*time.Minute {
		t.Fatal("budget applied twice")
	}
	if strings.Contains(meta.Transfer.Error, "deadline") {
		t.Fatal(meta.Transfer.Error)
	}
	e.wait(a.ID, 0) // The fake handoff turn must finish before its isolated files are removed.
}

func TestRoomDormantOwnershipPublicationOrders(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(map[bool]string{false: "reservation-first", true: "job-first"}[first], func(t *testing.T) {
			e := roomControlEnv(t)
			a, _ := e.start("SLEEP:1 RESPOND: idle")
			a, _ = e.wait(a.ID, 1)
			e.m.Store.UpdateMeta(a.ID, func(m *Meta) {
				m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				m.Status = StatusDormant
			})
			store := shells.Store{Root: filepath.Join(filepath.Dir(e.m.Store.root()), "shells")}
			publish := func() error {
				_, err := store.Create(shells.StartOptions{Cmd: []string{"fake-no-process"}, Cwd: a.Dir, Owner: a.ID})
				return err
			}
			if first {
				if err := publish(); err != nil {
					t.Fatal(err)
				}
				e.doneGoals()
				result, err := closeIdle(e.m, e.ctx, a.RoomID)
				if err != nil || result.Succeeded != 0 {
					t.Fatalf("closed job owner: %+v %v", result, err)
				}
			} else {
				token := NewID()
				if err := e.m.reserveDormant(e.ctx, a.ID, token, time.Now().Add(-testIdleAfter)); err != nil {
					t.Fatal(err)
				}
				if err := publish(); err == nil {
					t.Fatal("published while closing")
				}
				e.m.releaseDormant(a.ID, "wrong-token")
				if err := publish(); err == nil {
					t.Fatal("wrong token released reservation")
				}
				e.m.releaseDormant(a.ID, token)
				e.doneGoals()
				if result, err := closeIdle(e.m, e.ctx, a.RoomID); err != nil || result.Succeeded != 1 {
					t.Fatalf("safe close: %+v %v", result, err)
				}
				if err := publish(); err == nil {
					t.Fatal("published for closed owner")
				}
			}
		})
	}
}

func TestClearDormantSkipsWokenSession(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	// A wake clears Finished when its turn starts.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Status, m.Finished = StatusIdle, time.Time{} }); err != nil {
		t.Fatal(err)
	}
	ownerDismissEnv(t)
	if _, err := e.m.DismissSession(e.ctx, info.ID); err == nil || !strings.Contains(err.Error(), "no longer dormant") {
		t.Fatalf("must refuse woken session: %v", err)
	}
	cleared, err := e.m.ClearDormantSessions(e.ctx, info.RoomID)
	if err != nil || len(cleared.Results) != 0 || cleared.State.Dormant != 0 {
		t.Fatalf("empty clear: %+v %v", cleared, err)
	}
	if meta, err := e.m.Store.ReadMeta(info.ID); err != nil || !meta.Closed.IsZero() {
		t.Fatalf("woken session changed: %+v %v", meta, err)
	}
}

func TestClearDormantReportsGainedWorkAsANeutralKeptSession(t *testing.T) {
	e := roomControlEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, Kind: "message", To: []string{info.Address()}, Text: "new work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) {
		m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
		m.Status = StatusDormant
	}); err != nil {
		t.Fatal(err)
	}
	e.doneGoals()
	cleared, err := e.m.ClearDormantSessions(e.ctx, info.RoomID)
	if err != nil || len(cleared.Results) != 1 {
		t.Fatalf("clear dormant: %+v %v", cleared, err)
	}
	row := cleared.Results[0]
	if row.OK != true || row.Status != "kept" || row.Reason != RoomSessionKeptStartedWork || row.Name != info.Address() {
		t.Fatalf("kept row = %+v; want a neutral started-work reason and a name", row)
	}
	if cleared.Succeeded != 0 || cleared.Kept != 1 || cleared.Failed != 0 {
		t.Fatalf("clear counts = %+v; want one kept session and no failure", cleared)
	}
}

func TestDormantKeptRowSeparatesNewWorkFromAFailure(t *testing.T) {
	meta := Meta{ID: "ses_a", Room: "dyna", Name: "ts-guard"}
	work := keptRow(meta, keptForWork("the session started a turn"))
	if !work.OK || work.Reason != RoomSessionKeptStartedWork || work.Name != "dyna:ts-guard" || work.Status != "kept" {
		t.Fatalf("gained work row = %+v; want a neutral kept row with its name", work)
	}
	failure := keptRow(meta, errors.New("the session store could not be read"))
	if failure.OK || failure.Reason != RoomSessionKeptFailed || failure.Error == "" {
		t.Fatalf("failure row = %+v; want a real failure", failure)
	}
}

// testIdleAfter is the dormant age these tests treat as long dormant.
const testIdleAfter = 30 * time.Minute

// closeIdle runs the sweep's per-room close with the test dormant age.
func closeIdle(m *Manager, ctx context.Context, room string) (RoomSessionMutation, error) {
	return m.closeDormantSessions(ctx, room, m.store().now().Add(-testIdleAfter))
}
