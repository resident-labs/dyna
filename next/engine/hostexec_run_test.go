package engine

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHostexec struct {
	entered    chan struct{}
	release    chan struct{}
	mu         sync.Mutex
	started    bool
	stopped    bool
	stopCalled chan struct{}
}

func (f *fakeHostexec) RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error) {
	close(f.entered)
	<-f.release
	return []byte("ok"), 0, false, nil
}
func (f *fakeHostexec) StopHostexec(context.Context, contract.HostName, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started {
		f.stopped = true
	}
	f.stopCalled <- struct{}{}
	return nil
}
func TestHostexecSessionCloseStopsAndEndsGrant(t *testing.T) {
	s, room, worker, ctx := setup(t)
	runner := &fakeHostexec{entered: make(chan struct{}), release: make(chan struct{}), stopCalled: make(chan struct{}, 2)}
	s.SetHostexecRunner(runner)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	g := contract.Grant{ID: "grant_close", Kind: contract.GrantHostExec, Room: room, To: who, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: s.now(), Hosts: []contract.HostName{"host"}}
	if err := s.store.CreateGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Command: "true"})
		done <- e
	}()
	<-runner.entered
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: worker, Discard: true}); e != nil {
		t.Fatal(e)
	}
	<-runner.stopCalled
	grants, err := s.store.ListActiveGrants(ctx, room, who, s.now())
	if err != nil || len(grants) != 0 {
		t.Fatalf("closed grants: %+v %v", grants, err)
	}
	runner.mu.Lock()
	runner.started = true
	runner.mu.Unlock()
	if err := s.StopHostexecs(ctx); err != nil {
		t.Fatal(err)
	}
	<-runner.stopCalled
	runner.mu.Lock()
	stopped := runner.stopped
	runner.mu.Unlock()
	close(runner.release)
	if e := <-done; e != nil || !stopped {
		t.Fatalf("late run: stopped=%v, error=%v", stopped, e)
	}
}

// stoppableHostexec runs until release closes or its scope is stopped, which ends it as a stopped scope would, and passes
// on the scopes it is asked to stop. It ignores its context, as a run on the host goes on when its caller leaves.
type stoppableHostexec struct {
	entered chan struct{}
	release chan struct{}
	stopped chan struct{}
	stops   chan string
}

func newStoppableHostexec() stoppableHostexec {
	return stoppableHostexec{entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{}), stops: make(chan string, 4)}
}
func (f stoppableHostexec) RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error) {
	close(f.entered)
	select {
	case <-f.release:
		return []byte("finished\n"), 0, false, nil
	case <-f.stopped:
		return nil, 143, false, nil
	}
}
func (f stoppableHostexec) StopHostexec(_ context.Context, _ contract.HostName, scope string) error {
	f.stops <- scope
	close(f.stopped)
	return nil
}

func hostexecRun(t *testing.T, s *Service, ctx context.Context, runner stoppableHostexec) {
	t.Helper()
	s.SetHostexecRunner(runner)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func hostexecRow(t *testing.T, s *Service, op contract.OpID) (string, bool) {
	t.Helper()
	var stopped string
	var ended sql.NullInt64
	if err := s.store.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op=?", op).Scan(&stopped, &ended); err != nil {
		t.Fatal(err)
	}
	return stopped, ended.Valid
}

func opRanWakes(t *testing.T, s *Service, to contract.SessionID) int {
	t.Helper()
	var wakes int
	if err := s.store.Read().QueryRow("SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='op.ran' AND d.to_id=? AND d.wake=1", to).Scan(&wakes); err != nil {
		t.Fatal(err)
	}
	return wakes
}

// A stop names the run by the client ID its run sent. The run's scope is stopped on the host, the run is stored as
// stopped, and its waiting caller gets that result.
func TestHostexecStopEndsTheRun(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	runner := newStoppableHostexec()
	hostexecRun(t, s, ctx, runner)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	answered := make(chan contract.HostexecRunResponse, 1)
	go func() {
		res, _ := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Command: "find / -name x", Timeout: contract.DurationMS(time.Hour.Milliseconds()), ClientID: "run-1"})
		answered <- res
	}()
	<-runner.entered
	if _, problem := s.HostexecStop(ctx, who, contract.HostexecStopRequest{ClientID: "other"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("an unknown run: %+v", problem)
	}
	if _, problem := s.HostexecStop(ctx, who, contract.HostexecStopRequest{ClientID: "run-1"}); problem != nil {
		t.Fatal(problem)
	}
	op := (<-answered).Operation
	if scope := <-runner.stops; scope != "dyna-hostexec-"+string(op.ID)+".scope" {
		t.Fatalf("stopped %s", scope)
	}
	if op.Result == nil || op.Result.Error != "stopped by its session" || op.Result.Cause != "" {
		t.Fatalf("result %+v", op.Result)
	}
	if stopped, ended := hostexecRow(t, s, op.ID); stopped != string(store.HostexecStopped) || !ended {
		t.Fatalf("run stopped=%q ended=%v", stopped, ended)
	}
	// The run has ended, so a second stop does nothing.
	if _, problem := s.HostexecStop(ctx, who, contract.HostexecStopRequest{ClientID: "run-1"}); problem != nil {
		t.Fatal(problem)
	}
	if len(runner.stops) != 0 {
		t.Fatal("an ended run was stopped again")
	}
}

// A caller that goes away, as when the box's connection to the engine drops, leaves the run going. It ends as it would
// have, and its result wakes the session through op.ran.
func TestHostexecCallerLeavingLeavesTheRun(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	runner := newStoppableHostexec()
	hostexecRun(t, s, ctx, runner)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	call, leave := context.WithCancel(ctx)
	answered := make(chan contract.HostexecRunResponse, 1)
	go func() {
		res, _ := s.HostexecRun(call, who, contract.HostexecRunRequest{Host: "host", Command: "make", Timeout: contract.DurationMS(time.Hour.Milliseconds()), ClientID: "run-1"})
		answered <- res
	}()
	<-runner.entered
	leave()
	op := (<-answered).Operation.ID
	before := opRanWakes(t, s, root)
	close(runner.release)
	waitForOperation(t, s, ctx, op, contract.OpDone)
	if len(runner.stops) != 0 {
		t.Fatal("a caller that left stopped its run")
	}
	if stopped, ended := hostexecRow(t, s, op); stopped != "" || !ended {
		t.Fatalf("run stopped=%q ended=%v", stopped, ended)
	}
	record, err := s.store.Operation(ctx, op)
	if err != nil || record.Operation.Result == nil || record.Operation.Result.Output != "finished\n" {
		t.Fatalf("result %+v: %v", record.Operation.Result, err)
	}
	if wakes := opRanWakes(t, s, root); wakes != before+1 {
		t.Fatalf("op.ran wakes went from %d to %d", before, wakes)
	}
}

func TestHostexecNoGrant(t *testing.T) {
	s, _, worker, ctx := setup(t)
	_, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.HostexecRunRequest{Host: "host", Command: "true"})
	if e == nil || e.Code != contract.ErrNoGrant {
		t.Fatalf("got %+v", e)
	}
}

type completedHostexec struct {
	output []byte
	err    error
}

func (f completedHostexec) RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error) {
	return f.output, 0, false, f.err
}
func (completedHostexec) StopHostexec(context.Context, contract.HostName, string) error { return nil }

func TestHostexecRunsTheCommandWithBash(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetHostexecRunner(completedHostexec{output: []byte("up\n")})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	res, problem := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Command: "uptime | wc -l"})
	if problem != nil {
		t.Fatal(problem)
	}
	args := res.Operation.Args.(contract.HostexecRunArgs)
	if !slices.Equal(args.Argv, []string{"bash", "-c", "uptime | wc -l"}) || args.Room != room || res.Operation.Result.Output != "up\n" {
		t.Fatalf("operation %+v", res.Operation)
	}
	if _, problem := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Command: "  "}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("an empty command: %+v", problem)
	}
}

func TestHostexecLostFinishesOperationAndNotifies(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	s.SetHostexecRunner(completedHostexec{err: errors.New("master dropped")})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Command: "true"})
	if e != nil || res.Operation.Result == nil || !strings.Contains(res.Operation.Result.Error, "master dropped") {
		t.Fatalf("result %+v error %+v", res, e)
	}
	var count int
	if err := s.store.Read().QueryRow("SELECT count(*) FROM events WHERE type='op.ran'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("op.ran count %d: %v", count, err)
	}
	var stopped string
	var ended sql.NullInt64
	if err := s.store.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op=?", res.Operation.ID).Scan(&stopped, &ended); err != nil || stopped != "lost" || ended.Valid {
		t.Fatalf("stopped %s ended %+v: %v", stopped, ended, err)
	}
}
func TestHostexecOutputFileRoomAccess(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	s.SetHostexecRunner(completedHostexec{output: bytes.Repeat([]byte("a"), (64<<10)+1)})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Command: "true"})
	if e != nil || res.Operation.Result == nil || res.Operation.Result.OutputFile == "" {
		t.Fatalf("result %+v error %+v", res, e)
	}
	hash := res.Operation.Result.OutputFile
	for _, tc := range []struct {
		room contract.RoomID
		want int
	}{{room, http.StatusOK}, {"other", http.StatusNotFound}} {
		rec := httptest.NewRecorder()
		s.Files(contract.Principal{Kind: contract.PrincipalSession, Session: root}, tc.room).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/files/"+hash, nil))
		if rec.Code != tc.want {
			t.Fatalf("room %s status %d want %d", tc.room, rec.Code, tc.want)
		}
	}
}

func TestHostexecApprovalAndSessionCloseRecheck(t *testing.T) {
	s, room, worker, ctx := setup(t)
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	grant := contract.Grant{ID: "grant_test", Kind: contract.GrantHostExec, Room: room, To: principal, Hosts: []contract.HostName{"host"}, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: s.now()}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error { return s.store.InsertGrant(tx, grant) }); err != nil {
		t.Fatal(err)
	}
	s.SetHostexecRunner(completedHostexec{})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	result, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Command: "true"})
	if e != nil || result.Operation.Class != contract.ClassAuto || result.Operation.State != contract.OpDone {
		t.Fatalf("result %+v error %+v", result, e)
	}
	if _, e = s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: worker, Discard: true}); e != nil {
		t.Fatal(e)
	}
	if err := s.HostexecRunEntry().Check(ctx, result.Operation.Args, principal); err == nil {
		t.Fatal("closed session grant passed recheck")
	}
}
