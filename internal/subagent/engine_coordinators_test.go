package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/sessionlife/recordtest"
)

// coordinatorFailureBound limits a worker join or a lock-wait signal.
// Workers.Wait and the select return when the event arrives.
const coordinatorFailureBound = 30 * time.Second

func coordinatorWorkers(t *testing.T, e *testEnv) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	workers := NewWorkers(ctx, t.Logf)
	e.m.Workers = workers
	e.m.Executable = filepath.Join(t.TempDir(), "no-such-dyna")
	t.Cleanup(func() {
		cancel()
		if left := workers.Wait(coordinatorFailureBound); len(left) > 0 {
			t.Errorf("coordinators did not stop: %v", left)
		}
	})
	return cancel
}

func waitCoordinators(t *testing.T, e *testEnv) {
	t.Helper()
	if left := e.m.Workers.Wait(coordinatorFailureBound); len(left) > 0 {
		t.Fatalf("coordinators did not finish: %v", left)
	}
}

// Startup can wait for the replacement runner. Join its real completion
// event instead of advancing a test clock or polling with fixed sleeps.
func waitCoordinatorTurn(e *testEnv, target string, n int) func(context.Context, time.Duration) error {
	spawner := e.m.Spawner.(*joinedFixtureSpawner)
	return func(ctx context.Context, _ time.Duration) error {
		done, ok := spawner.turns.Load(fixtureTurn{target, n})
		if !ok {
			return fmt.Errorf("coordinator waited before replacement turn %d started", n)
		}
		select {
		case <-done.(chan struct{}):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestEngineResumesConsolidationInProcess(t *testing.T) {
	for _, phase := range []string{"launch", "waiting", "stopping", "starting"} {
		t.Run(phase, func(t *testing.T) {
			e, _ := consolidationEnv(t)
			a, b := completedFixtureSession(t, e), completedFixtureSession(t, e)
			c := consolidatePair(t, e, a, b)
			submitAll(t, e, c)
			if phase != "launch" {
				for c.Phase != phase {
					c = consolidationStep(t, e, c)
				}
			}
			e.m.ConsolidationLaunch = nil
			e.m.TransferSleep = waitCoordinatorTurn(e, a.ID, 2)
			t.Cleanup(authority.InEngine())
			coordinatorWorkers(t, e)
			if phase == "launch" {
				if err := e.m.launchConsolidation(c); err != nil {
					t.Fatalf("consolidation tried to start a detached process: %v", err)
				}
			} else if err := e.m.ResumeWorkersForEngine(); err != nil {
				t.Fatal(err)
			}
			waitCoordinators(t, e)
			meta, err := e.m.Store.ReadMeta(a.ID)
			if err != nil || meta.Consolidation.Phase != "completed" || meta.Turns != 2 {
				t.Fatalf("after restart: consolidation %+v, turns %d, %v", meta.Consolidation, meta.Turns, err)
			}
			source, err := e.m.Store.ReadMeta(b.ID)
			if err != nil || source.Closed.IsZero() || source.ConsumedBy != a.ID {
				t.Fatalf("source was not absorbed once: %+v, %v", source, err)
			}
			if err := e.m.ResumeWorkersForEngine(); err != nil {
				t.Fatal(err)
			}
			waitCoordinators(t, e)
			meta, err = e.m.Store.ReadMeta(a.ID)
			if err != nil || meta.Turns != 2 {
				t.Fatalf("a repeated sweep started another turn: %d, %v", meta.Turns, err)
			}
		})
	}
}

func TestRoomResumeUsesEngineWorkersOnce(t *testing.T) {
	e := roomControlEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.PauseRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Status, turn.Ended = TurnQueued, time.Time{}
		turn.RoomHeld = true
	}); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	e.m.Spawner = recordRoomSpawn{start: func(m *Manager, id string, n int, lock *os.File) error {
		defer lock.Close()
		starts.Add(1)
		// recoverHeldTurn calls Spawn while it owns the session lock.
		// Model immediate admission without taking that lock a second time.
		turn, err := m.Store.readTurnForUpdate(id, n)
		if err != nil {
			return err
		}
		turn.Status, turn.RoomHeld, turn.HeldAccepted = TurnRunning, false, true
		return m.Store.writeTurn(id, turn)
	}}
	e.m.ResumeRoomLaunch = nil
	coordinatorWorkers(t, e)
	if _, err := e.m.ResumeRoomSessions(e.ctx, info.RoomID); err != nil {
		t.Fatalf("room resume tried to start a detached process: %v", err)
	}
	waitCoordinators(t, e)
	pause, err := e.m.rooms().SessionPause(info.RoomID)
	if err != nil || pause.ResumePending || starts.Load() != 1 {
		t.Fatalf("resume pending=%v, starts=%d, %v", pause.ResumePending, starts.Load(), err)
	}
	if err := e.m.launchRoomResume(info.RoomID, pause.Revision); err != nil {
		t.Fatal(err)
	}
	waitCoordinators(t, e)
	if starts.Load() != 1 {
		t.Fatalf("repeated resume started %d runners", starts.Load())
	}
}

// An earlier release can still hold a detached worker lock during upgrade.
// Engine stop cancels that wait, and the next start resumes the saved work.
func TestEngineCoordinatorsCancelOldWorkerLockWait(t *testing.T) {
	for _, kind := range []string{"consolidation", "room-resume"} {
		t.Run(kind, func(t *testing.T) {
			e, _ := consolidationEnv(t)
			info := completedFixtureSession(t, e)
			var launch func() error
			var unlock func()
			var err error
			if kind == "consolidation" {
				other := completedFixtureSession(t, e)
				c := consolidatePair(t, e, info, other)
				submitAll(t, e, c)
				e.m.ConsolidationLaunch = nil
				launch = func() error { return e.m.launchConsolidation(c) }
				unlock, err = e.m.consolidationWorkerLock(info.ID)
			} else {
				if _, err := e.m.rooms().SetSessionsPaused(e.ctx, info.RoomID, true); err != nil {
					t.Fatal(err)
				}
				pause, err := e.m.rooms().SetSessionsPaused(e.ctx, info.RoomID, false)
				if err != nil {
					t.Fatal(err)
				}
				e.m.ResumeRoomLaunch = nil
				launch = func() error { return e.m.launchRoomResume(info.RoomID, pause.Revision) }
				var lock *os.File
				lock, err = os.OpenFile(filepath.Join(e.m.Store.root(), ".resume-"+info.RoomID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err == nil {
					err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
					unlock = func() { _ = lock.Close() }
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { unlock() }()
			waiting := make(chan struct{}, 1)
			e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
				select {
				case waiting <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return ctx.Err()
			}
			cancel := coordinatorWorkers(t, e)
			if err := launch(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-waiting:
			case <-time.After(coordinatorFailureBound):
				t.Fatal("coordinator did not wait for the old workers lock")
			}
			cancel()
			waitCoordinators(t, e)
			unlock()
			unlock = func() {}
			coordinatorWorkers(t, e)
			if kind == "consolidation" {
				e.m.TransferSleep = waitCoordinatorTurn(e, info.ID, 2)
			}
			if err := launch(); err != nil {
				t.Fatal(err)
			}
			waitCoordinators(t, e)
			if kind == "consolidation" {
				meta, err := e.m.Store.ReadMeta(info.ID)
				if err != nil || meta.Consolidation.Phase != "completed" || meta.Turns != 2 {
					t.Fatalf("consolidation did not resume once: %+v, %v", meta, err)
				}
			} else {
				pause, err := e.m.rooms().SessionPause(info.RoomID)
				if err != nil || pause.ResumePending {
					t.Fatalf("room resume did not finish: %+v, %v", pause, err)
				}
			}
		})
	}
}

func TestEngineSweepReplaysConsolidationCommit(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, b := completedFixtureSession(t, e), completedFixtureSession(t, e)
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	c = consolidationStep(t, e, c)
	c = consolidationStep(t, e, c)
	// The replacement has started, but the engine stopped before archiving
	// sources. The persisted charter and source plan already exist.
	if _, err := e.m.Store.UpdateMeta(a.ID, func(meta *Meta) {
		meta.Consolidation.Phase = "committing"
		meta.Transfer.Phase = "completed"
		meta.Turns = meta.Transfer.Turn
	}); err != nil {
		t.Fatal(err)
	}
	e.m.ConsolidationLaunch = nil
	t.Cleanup(authority.InEngine())
	coordinatorWorkers(t, e)
	for range 2 {
		if err := e.m.ResumeWorkersForEngine(); err != nil {
			t.Fatal(err)
		}
		waitCoordinators(t, e)
	}
	kept, err := e.m.Store.ReadMeta(a.ID)
	if err != nil || kept.Consolidation.Phase != "completed" || kept.Turns != 2 {
		t.Fatalf("commit did not resume once: %+v, %v", kept, err)
	}
	source, err := e.m.Store.ReadMeta(b.ID)
	if err != nil || source.Closed.IsZero() || source.ConsumedBy != a.ID {
		t.Fatalf("commit did not archive the source: %+v, %v", source, err)
	}
}

// A damaged session must not prevent later durable work from restarting.
func TestEngineSweepSkipsUnreadableConsolidation(t *testing.T) {
	e, _ := consolidationEnv(t)
	good := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateMeta(good.ID, func(meta *Meta) {
		meta.Consolidation = &ConsolidationState{ID: "good", Target: good.ID, Phase: "waiting"}
	}); err != nil {
		t.Fatal(err)
	}
	// This valid JSON row matches the phase query, but Profile cannot decode
	// into a session. Its ID sorts before the healthy session.
	const badID = "ses_20000101-000000-00000000"
	recordtest.Write(t, e.m.Store.root(), badID, struct {
		Meta
		Profile int `json:"profile"`
	}{Meta: Meta{ID: badID, RoomID: e.room.ID, Room: e.room.Name,
		Consolidation: &ConsolidationState{ID: "bad", Target: badID, Phase: "waiting"}}, Profile: 1})
	var resumed, logged string
	e.m.ConsolidationLaunch = func(_ *Manager, target, _ string) error {
		resumed = target
		return nil
	}
	e.m.Workers = NewWorkers(e.ctx, func(format string, args ...any) { logged = fmt.Sprintf(format, args...) })
	if err := e.m.ResumeWorkersForEngine(); err != nil {
		t.Fatalf("one unreadable session stopped the sweep: %v", err)
	}
	if resumed != good.ID || !strings.Contains(logged, badID) {
		t.Fatalf("resumed %q, log %q; want healthy worker and damaged session report", resumed, logged)
	}
}
