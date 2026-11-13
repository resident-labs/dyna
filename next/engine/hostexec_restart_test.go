package engine

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/harness/testutil"

	boxhostexec "github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

// restartHostexec is a host whose run goes on when its caller goes away, as a command in a systemd scope does. A start waits
// until release closes or its context ends (the engine stopping), and a follow answers as the host would after the restart.
type restartHostexec struct {
	mu       sync.Mutex
	runs     []contract.HostexecRunArgs
	stops    int
	started  chan struct{}
	returned chan struct{}
	release  chan struct{}
	follow   func(contract.OpID) ([]byte, int, bool, error)
	once     sync.Once
}

func newRestartHostexec(follow func(contract.OpID) ([]byte, int, bool, error)) *restartHostexec {
	return &restartHostexec{started: make(chan struct{}), returned: make(chan struct{}), release: make(chan struct{}), follow: follow}
}

func (f *restartHostexec) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	f.mu.Lock()
	f.runs = append(f.runs, a)
	f.mu.Unlock()
	switch {
	case a.Collect:
		return nil, 0, false, nil
	case a.Attach:
		return f.follow(op)
	}
	close(f.started)
	defer f.once.Do(func() { close(f.returned) })
	select {
	case <-f.release:
		return []byte("finished\n"), 0, false, nil
	case <-ctx.Done():
		return nil, 0, false, ctx.Err()
	}
}
func (f *restartHostexec) StopHostexec(context.Context, contract.HostName, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

// hostRunThenRestart starts a host run, stops the engine under it (its shutdown context ends, and so does the box's call), and
// starts it again on the same store. It returns the run's operation ID once the new engine's startup has run.
func hostRunThenRestart(t *testing.T, s *Service, ctx context.Context, who contract.Principal, runner *restartHostexec, timeout contract.DurationMS, beforeRestart func()) contract.OpID {
	t.Helper()
	kinds := map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}
	s.SetHostexecRunner(runner)
	engine, shutdown := context.WithCancel(ctx)
	defer shutdown()
	if err := s.ConfigureOperations(engine, kinds, time.Hour); err != nil {
		t.Fatal(err)
	}
	call, dropCall := context.WithCancel(ctx)
	answered := make(chan contract.HostexecRunResponse, 1)
	go func() {
		res, _ := s.HostexecRun(call, who, contract.HostexecRunRequest{Host: "host", Command: "make deploy", Timeout: timeout, ClientID: "run-1"})
		answered <- res
	}()
	<-runner.started
	shutdown()
	dropCall()
	res := <-answered
	<-runner.returned
	synctest.Wait() // Join the old in-process executor before observing its persisted state.
	if res.Operation.Result != nil {
		t.Fatalf("the call was answered with a result while the run went on: %+v", res.Operation.Result)
	}
	record, err := s.store.ReadReplayOperation(ctx, who, "run-1")
	if err != nil || record.Operation.State != contract.OpRunning {
		t.Fatalf("after the shutdown the operation is %+v: %v", record.Operation, err)
	}
	if stopped, ended := hostexecRow(t, s, record.Operation.ID); stopped != "" || ended {
		t.Fatalf("after the shutdown the run is marked %q, ended %v", stopped, ended)
	}
	beforeRestart()
	if err := s.ConfigureOperations(ctx, kinds, time.Hour); err != nil {
		t.Fatal(err)
	}
	return record.Operation.ID
}

func waitForResult(t *testing.T, s *Service, ctx context.Context, who contract.Principal) contract.Operation {
	t.Helper()
	var op contract.Operation
	testutil.Eventually(t, 5*time.Second, "host operation result", func() bool {
		record, err := s.store.ReadReplayOperation(ctx, who, "run-1")
		if err != nil {
			t.Fatal(err)
		}
		op = record.Operation
		return op.Result != nil
	})
	return op
}

// The engine restarts (a deploy) under a host run. The run is neither failed nor stopped: the new engine follows it on its host
// and finishes it with the output and exit status the command had.
func TestHostRunSurvivesAnEngineRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) { return []byte("all of the output\n"), 7, false, nil })
		op := hostRunThenRestart(t, s, ctx, who, runner, 0, func() {})
		got := waitForResult(t, s, ctx, who)
		if got.State != contract.OpFailed || got.Result.Output != "all of the output\n" || got.Result.ExitCode != 7 || got.Result.Error != "" {
			t.Fatalf("operation %+v result %+v", got, got.Result)
		}
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if runner.stops != 0 || len(runner.runs) != 3 || runner.runs[0].Attach || !runner.runs[1].Attach || !runner.runs[2].Collect {
			t.Fatalf("stops %d, runs %+v", runner.stops, runner.runs)
		}
		if stopped, ended := hostexecRow(t, s, op); stopped != "" || !ended {
			t.Fatalf("run row: stopped %q ended %v", stopped, ended)
		}

	})
}

func TestEngineHostRunSurvivesAnEngineRestart(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	if err := s.store.SetEngineHost(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) { return []byte("engine host output\n"), 7, false, nil })
	hostRunThenRestart(t, s, ctx, who, runner, 0, func() {})
	got := waitForResult(t, s, ctx, who)
	if got.Result.Output != "engine host output\n" || got.Result.ExitCode != 7 || got.Result.Error != "" {
		t.Fatalf("operation %+v result %+v", got, got.Result)
	}
}

// A resume that cannot follow ends with the restart as its cause and names the host and the unit, never a bare context error.
func TestHostRunThatCannotBeFollowedAfterARestartEndsNamingTheRestart(t *testing.T) {
	for name, follow := range map[string]func(contract.OpID) ([]byte, int, bool, error){
		"host does not answer": func(contract.OpID) ([]byte, int, bool, error) { return nil, 0, false, errors.New("ssh: no route") },
		"host lost its files": func(op contract.OpID) ([]byte, int, bool, error) {
			return []byte(boxhostexec.NoRunOutput(op)), boxhostexec.NoRunStatus, false, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, _, root, ctx := setupRoot(t)
				s.hostexecTimeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
					return context.WithTimeout(ctx, 300*time.Millisecond)
				}
				who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
				op := hostRunThenRestart(t, s, ctx, who, newRestartHostexec(follow), 0, func() {})
				got := waitForResult(t, s, ctx, who)
				want := "the engine restarted during this host run (deploy); the command may still be running on host as dyna-hostexec-" + string(op) + ".scope"
				if got.State != contract.OpFailed || got.Result.Cause != contract.CauseEngineRestarted || got.Result.Error != want || got.Result.ExitCode != 0 {
					t.Fatalf("operation %+v result %+v", got, got.Result)
				}
			})
		})
	}

}

// The box asks again with its client ID while the run is going: the answer waits for the run and carries its result, and a
// result that a waiting call took does not also wake the session.
func TestAnAskedAgainHostRunWaitsForTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		runner := newRestartHostexec(nil)
		s.SetHostexecRunner(runner)
		if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
			t.Fatal(err)
		}
		call, dropCall := context.WithCancel(ctx)
		go func() {
			_, _ = s.HostexecRun(call, who, contract.HostexecRunRequest{Host: "host", Command: "make", ClientID: "run-1"})
		}()
		<-runner.started
		dropCall()
		again := make(chan contract.HostexecRunResponse, 1)
		go func() {
			res, _ := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Command: "make", ClientID: "run-1"})
			again <- res
		}()
		synctest.Wait() // The replay caller has joined the running operation.
		close(runner.release)
		select {
		case res := <-again:
			if res.Operation.Result == nil || res.Operation.Result.Output != "finished\n" {
				t.Fatalf("answer %+v", res.Operation)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no answer")
		}
		var wakes int
		if err := s.store.Read().QueryRow("SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='op.ran'", root).Scan(&wakes); err != nil || wakes != 0 {
			t.Fatalf("wakes %d: %v", wakes, err)
		}

	})
}

// A command that exits with the status the host's "no files" answer has, but not with its words, is a command's own exit.
func TestAResumedCommandThatExitsWithTheNoRunStatusIsItsOwnExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) {
			return []byte("my own words\n"), boxhostexec.NoRunStatus, false, nil
		})
		hostRunThenRestart(t, s, ctx, who, runner, 0, func() {})
		got := waitForResult(t, s, ctx, who)
		if got.Result.Cause != "" || got.Result.ExitCode != boxhostexec.NoRunStatus || got.Result.Output != "my own words\n" {
			t.Fatalf("result %+v", got.Result)
		}

	})
}

// The follow after a restart is bounded by what is left of the run's time counted from its start, and a call to a host that does
// not answer is made again until then.
func TestResumeIsBoundedByTheRunsStartAndAsksTheHostAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		var clock atomic.Int64
		clock.Store(time.Now().UnixMilli())
		s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
		var mu sync.Mutex
		var bounds []time.Duration
		s.hostexecTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			mu.Lock()
			bounds = append(bounds, d)
			mu.Unlock()
			return context.WithTimeout(ctx, 30*time.Second)
		}
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		var asked atomic.Int32
		runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) {
			if asked.Add(1) == 1 {
				return nil, 0, false, errors.New("ssh: no route")
			}
			return []byte("done\n"), 0, false, nil
		})
		hostRunThenRestart(t, s, ctx, who, runner, contract.DurationMS(time.Hour.Milliseconds()), func() { clock.Add(10 * time.Minute.Milliseconds()) })
		got := waitForResult(t, s, ctx, who)
		if got.Result.Cause != "" || got.Result.Output != "done\n" || asked.Load() != 2 {
			t.Fatalf("asked %d times, result %+v", asked.Load(), got.Result)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(bounds) < 2 || bounds[1] != 50*time.Minute+15*time.Second {
			t.Fatalf("the follow was bounded by %v", bounds)
		}

	})
}

// A host that does not answer until the run's time is up is asked until then, and the end names the restart.
func TestAHostThatNeverAnswersIsAskedUntilTheRunsTimeIsUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		s.hostexecTimeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 1200*time.Millisecond)
		}
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) { return nil, 0, false, errors.New("ssh: no route") })
		hostRunThenRestart(t, s, ctx, who, runner, 0, func() {})
		got := waitForResult(t, s, ctx, who)
		runner.mu.Lock()
		defer runner.mu.Unlock()
		var follows int
		for _, a := range runner.runs {
			if a.Attach {
				follows++
			}
		}
		if got.Result.Cause != contract.CauseEngineRestarted || follows < 3 {
			t.Fatalf("cause %q after %d follows", got.Result.Cause, follows)
		}

	})
}

// A grant that ended while the engine was away ends the run as stale, and its command is stopped like a session's stop.
func TestAResumedRunWhoseGrantEndedIsStaleAndStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, root, ctx := setupRoot(t)
		who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		runner := newRestartHostexec(func(contract.OpID) ([]byte, int, bool, error) { return []byte("never read\n"), 0, false, nil })
		op := hostRunThenRestart(t, s, ctx, who, runner, 0, func() {
			err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE grants SET revoked_at=? WHERE kind='hostexec'", time.Now().UnixMilli())
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
		got := waitForResult(t, s, ctx, who)
		if got.Result.Cause != contract.CauseStale || !strings.HasPrefix(got.Result.Error, "stale: ") {
			t.Fatalf("result %+v", got.Result)
		}
		if stopped, _ := hostexecRow(t, s, op); stopped != "stopped" {
			t.Fatalf("run %q", stopped)
		}
		runner.mu.Lock()
		defer runner.mu.Unlock()
		for _, a := range runner.runs {
			if a.Attach {
				t.Fatal("a run without a grant was followed")
			}
		}

	})
}
