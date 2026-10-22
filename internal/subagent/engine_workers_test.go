package subagent

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/authority"
)

// handoffAnswered stores a transfer whose old agent ended its turn with a
// [DYNA HANDOFF] answer after the ask went out.
func handoffAnswered(t *testing.T, e *testEnv) (Info, TransferState) {
	t.Helper()
	info, _ := e.start("SLEEP:1 RESPOND: original")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.updateTransfer(info.ID, state.ID, func(_ *Meta, t *TransferState) error { t.Posted = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Result = "[DYNA HANDOFF]\nNext: finish the parser."
		turn.Ended = time.Now().Add(time.Second)
	}); err != nil {
		t.Fatal(err)
	}
	return info, state
}

// shortSleep is a coordinator sleep that ends with its context.
func shortSleep(ctx context.Context, _ time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Millisecond):
		return nil
	}
}

// In the engine a coordinator has no caller on its context. It still takes
// the old agent's [DYNA HANDOFF] answer, as the engine's own work, while
// the checked SubmitHandoff refuses a context with no caller.
func TestCoordinatorTakesTheHandoffAnswerInTheEngine(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, state := handoffAnswered(t, e)
	t.Cleanup(authority.InEngine())
	if _, err := e.m.advanceTransfer(context.Background(), info.ID, state.ID); err != nil {
		t.Fatalf("the coordinator could not take the handoff answer: %v", err)
	}
	if got, _ := e.m.TransferStatus(info.ID); got.Source != "cooperative" || got.Handoff == "" {
		t.Fatalf("transfer = %+v; want the answer as a written handoff", got)
	}
	if err := e.m.SubmitHandoff(context.Background(), info.ID, state.ID, "late", "", ""); err == nil {
		t.Fatal("SubmitHandoff accepted a call with no caller")
	}
}

// As the engine starts, its sweep resumes a transfer whose coordinator
// stopped, from the stored state, in the engine and with no detached
// process. It starts one replacement turn, and a second sweep starts none.
func TestEngineSweepResumesAStoppedTransfer(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, _ := handoffAnswered(t, e)
	e.m.TransferLaunch = nil
	e.m.TransferSleep = shortSleep
	e.m.Executable = filepath.Join(t.TempDir(), "no-such-dyna") // a detached worker would fail to start
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.m.Workers = NewWorkers(ctx, t.Logf)
	for range 2 {
		if err := e.m.ResumeWorkersForEngine(); err != nil {
			t.Fatal(err)
		}
		if left := e.m.Workers.Wait(5 * time.Second); len(left) > 0 {
			t.Fatalf("workers still running: %v", left)
		}
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Transfer.Phase != "completed" || meta.Turns != 2 {
		t.Fatalf("after the sweeps: transfer %+v, turns %d, %v; want completed with one replacement turn", meta.Transfer, meta.Turns, err)
	}
}

// Engine stop cancels the coordinators and does not wait for a handoff
// wait, which can last two minutes. The transfer stays active for the next
// start's sweep.
func TestEngineStopDoesNotWaitForAHandoff(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: original")
	info, _ = e.wait(info.ID, 1)
	waiting := make(chan struct{}, 1)
	e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case waiting <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.m.Workers = NewWorkers(ctx, t.Logf)
	e.m.TransferLaunch = nil
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	<-waiting
	cancel()
	start := time.Now()
	if left := e.m.Workers.Wait(time.Second); len(left) > 0 {
		t.Fatalf("workers still running after stop: %v", left)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("stop waited %v for a handoff wait", took)
	}
	if got, _ := e.m.TransferStatus(info.ID); got == nil || got.ID != state.ID || !got.Active() {
		t.Fatalf("transfer after stop = %+v; want it still active for the next sweep", got)
	}
}

// A coordinator of the earlier release may still hold the transfer lock
// during an upgrade. The engine's coordinator waits for it, then goes on.
func TestCoordinatorWaitsForAnOldWorkersLock(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, state := handoffAnswered(t, e)
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.OpenFile(filepath.Join(dir, "transfer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := syscall.Flock(int(old.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{}, 1)
	e.m.TransferSleep = func(ctx context.Context, d time.Duration) error {
		select {
		case blocked <- struct{}{}:
		default:
		}
		return shortSleep(ctx, d)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.m.Workers = NewWorkers(ctx, t.Logf)
	if err := goTransfer(e.m, info.ID, state.ID); err != nil {
		t.Fatal(err)
	}
	<-blocked
	if got, _ := e.m.TransferStatus(info.ID); got.Handoff != "" {
		t.Fatalf("the coordinator went on while the old worker held the lock: %+v", got)
	}
	if err := syscall.Flock(int(old.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if left := e.m.Workers.Wait(5 * time.Second); len(left) > 0 {
		t.Fatalf("workers still running: %v", left)
	}
	if got, _ := e.m.TransferStatus(info.ID); got.Phase != "completed" {
		t.Fatalf("transfer = %+v; want completed after the old worker left", got)
	}
}

// Workers runs one worker per key, and Wait names a worker that outlives
// its bound.
func TestWorkersRunOncePerKeyAndNameTheOnesLeft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewWorkers(ctx, t.Logf)
	release, started := make(chan struct{}), make(chan struct{}, 2)
	for range 2 {
		w.Go("transfer ses_a", func(context.Context) error { started <- struct{}{}; <-release; return nil })
	}
	<-started
	if left := w.Wait(10 * time.Millisecond); len(left) != 1 || left[0] != "transfer ses_a" {
		t.Fatalf("left = %v; want the one worker", left)
	}
	close(release)
	if left := w.Wait(time.Second); len(left) != 0 {
		t.Fatalf("left = %v after release", left)
	}
	if len(started) != 0 {
		t.Fatal("a second worker ran for the same key")
	}
}
