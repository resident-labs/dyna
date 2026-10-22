package subagent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

func TestCloseDuringStartupClaimRecordsAStoppedTurn(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Status = TurnQueued
		turn.Started, turn.Ended = time.Time{}, time.Time{}
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.LockContext(e.ctx, filepath.Join(dir, "gate.lock"))
	if err != nil {
		t.Fatal(err)
	}
	unlock = sync.OnceFunc(unlock)
	defer unlock()
	lock, err := e.m.Store.tryTurnLock(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	contended := make(chan struct{})
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	ctx = store.WithFlockContentionHook(ctx, func(path string) {
		if path == filepath.Join(dir, "gate.lock") {
			close(contended)
		}
	})
	runnerDone := make(chan struct{})
	var runErr error
	go func() {
		runErr = e.m.RunTurn(ctx, info.ID, 1, lock)
		close(runnerDone)
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		select {
		case <-runnerDone:
		case <-time.After(5 * time.Second):
			t.Error("startup runner did not stop")
		}
	})
	select {
	case <-contended:
	case <-time.After(5 * time.Second):
		t.Fatal("startup claim did not wait for the gate lock")
	}
	closed, err := e.m.Close(e.ctx, info.ID)
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("close = %s, %v; want closed", closed.Status, err)
	}
	cancel()
	unlock()
	select {
	case <-runnerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("closed startup runner did not return")
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.Status != TurnStopped || !turn.StopAsked || turn.StopReason != "close" || !turn.Started.IsZero() {
		t.Fatalf("closed startup turn = %+v, %v; want stopped before start", turn, err)
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("runner result = %v; want context.Canceled", runErr)
	}
}

// A stop can commit while the queued runner has no pid. Its first turn
// read must cancel it before profile loading or harness work begins.
func TestStopBeforeRunnerRegistrationIsHonouredAtStart(t *testing.T) {
	e := newTestEnv(t)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: must not run")
	t.Cleanup(func() { held.lock.Close() })
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.StopAsked = true }); err != nil {
		t.Fatal(err)
	}
	loaded := false
	e.m.Profiles = func() (*profile.Store, error) {
		loaded = true
		return nil, errors.New("work began after the stop request")
	}
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); !errors.Is(err, context.Canceled) {
		t.Fatalf("runner result = %v; want context.Canceled", err)
	}
	if loaded {
		t.Fatal("the stopped runner loaded its profile")
	}
	if turn, err := e.m.Store.ReadTurn(info.ID, 1); err != nil || turn.Status != TurnStopped {
		t.Fatalf("turn = %s, %v; want stopped", turn.Status, err)
	}
}

// The fake process waits only for cancellation, so no timer controls when
// the harness starts or ends.
func startedStopFixture(t *testing.T) (*testEnv, Info, <-chan struct{}) {
	t.Helper()
	e := newTestEnv(t, profile.Profile{Name: "hold", Harness: profile.HarnessCustom, Command: []string{"/bin/true"}})
	started, cancelled := make(chan struct{}), make(chan struct{})
	e.m.ProcessRunner = func(ctx context.Context, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	info, _ := e.start("wait for stop", func(opts *StartOptions) { opts.Profile = "hold" })
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the harness did not start")
	}
	return e, info, cancelled
}

func TestLiveStopCancelsWithoutAMarker(t *testing.T) {
	e, info, cancelled := startedStopFixture(t)
	turn, err := e.m.Stop(e.ctx, info.ID)
	if err != nil || turn.Status != TurnStopped || !turn.StopAsked {
		t.Fatalf("stop = %s, asked %v, %v; want stopped", turn.Status, turn.StopAsked, err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("stop returned before cancelling the harness")
	}
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stop-1")); !os.IsNotExist(err) {
		t.Fatalf("stop marker exists: %v", err)
	}
}

// A stop that raced with the runner's initial read is checked again when
// its pid arrives at the engine.
func TestRunnerRegistrationHonoursTheStoredStop(t *testing.T) {
	e, info, cancelled := startedStopFixture(t)
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.StopAsked = true }); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: info.ID})
	if err := e.m.RunnerSetPid(ctx, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(30 * time.Second):
		t.Fatal("runner registration did not deliver the stored stop")
	}
}

// Without a marker, a failed intent write cannot be silently ignored while
// a queued runner has no pid to signal.
func TestStopReportsFailureToStoreItsIntent(t *testing.T) {
	e := newTestEnv(t)
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: queued")
	t.Cleanup(func() { held.lock.Close() })
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_stop BEFORE UPDATE ON session_turns
WHEN json_extract(NEW.turn, '$.stopAsked') = 1
BEGIN SELECT RAISE(ABORT, 'refuse stop'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := e.m.Stop(ctx, info.ID); err == nil || !strings.Contains(err.Error(), "refuse stop") {
		t.Fatalf("stop = %v; want the failed intent write", err)
	}
}
