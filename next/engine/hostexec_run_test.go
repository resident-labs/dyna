package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/resident-labs/dyna/box"
	boxhostexec "github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
	"net/http"
	"net/http/httptest"
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
		_, e := s.HostexecRun(ctx, who, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
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

func TestHostexecNoGrant(t *testing.T) {
	s, _, worker, ctx := setup(t)
	_, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
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

func TestHostexecBusyFinishesRun(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	s.SetHostexecRunner(completedHostexec{err: boxhostexec.ErrHostBusy})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, problem := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
	if problem != nil || res.Operation.Result == nil || res.Operation.Result.Cause != contract.CauseHostBusy || !strings.Contains(res.Operation.Result.Error, "retry") {
		t.Fatalf("busy result %+v: %v", res, problem)
	}
	var stopped string
	var ended sql.NullInt64
	if err := s.store.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op=?", res.Operation.ID).Scan(&stopped, &ended); err != nil || stopped != "" || !ended.Valid {
		t.Fatalf("busy run stopped=%q ended=%+v: %v", stopped, ended, err)
	}
}

func TestHostexecLostFinishesOperationAndNotifies(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	s.SetHostexecRunner(completedHostexec{err: errors.New("master dropped")})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
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
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
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

type timeoutHostexec struct{ advance func() }

func (f timeoutHostexec) RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error) {
	f.advance()
	return nil, 143, false, nil
}
func (timeoutHostexec) StopHostexec(context.Context, contract.HostName, string) error { return nil }
func TestHostexecSignalAtDeadlineIsTimeout(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	now := s.now()
	s.now = func() time.Time { return now }
	s.SetHostexecRunner(timeoutHostexec{advance: func() { now = now.Add(time.Minute) }})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}, Timeout: contract.DurationMS(time.Minute.Milliseconds())})
	if e != nil {
		t.Fatal(e)
	}
	var stopped string
	if err := s.store.Read().QueryRow("SELECT stopped FROM hostexec_runs WHERE op=?", res.Operation.ID).Scan(&stopped); err != nil || stopped != "timeout" {
		t.Fatalf("stopped %q: %v", stopped, err)
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
	result, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
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

type captureHostexec struct{ args contract.HostexecRunArgs }

func (f *captureHostexec) RunHostexec(_ context.Context, a contract.HostexecRunArgs, _ contract.OpID) ([]byte, int, bool, error) {
	f.args = a
	return nil, 0, false, nil
}
func (*captureHostexec) StopHostexec(context.Context, contract.HostName, string) error { return nil }
func TestHostexecTimeoutDefaultAndMaximum(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	runner := &captureHostexec{}
	s.SetHostexecRunner(runner)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	if _, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}}); e != nil {
		t.Fatal(e)
	}
	if runner.args.Timeout != contract.DurationMS((10 * time.Minute).Milliseconds()) {
		t.Fatalf("timeout %d", runner.args.Timeout)
	}
	if _, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}, Timeout: contract.DurationMS((2*time.Hour + time.Millisecond).Milliseconds())}); e == nil {
		t.Fatal("accepted timeout over two hours")
	}
}

type blockedStops struct {
	second  chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (f *blockedStops) RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error) {
	return nil, 0, false, nil
}
func (f *blockedStops) StopHostexec(ctx context.Context, _ contract.HostName, scope string) error {
	defer func() { f.done <- struct{}{} }()
	if scope == "one" {
		select {
		case <-f.release:
		case <-ctx.Done():
		}
		return nil
	}
	close(f.second)
	return nil
}
func TestHostexecStopsDoNotBlockEachOther(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	f := &blockedStops{second: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}, 2)}
	s.SetHostexecRunner(f)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	grants, err := s.store.ListActiveGrants(ctx, room, who, s.now())
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range []contract.OpID{"one", "two"} {
			op := store.StoredOperation{Operation: contract.Operation{ID: id, Kind: "hostexec.run", Room: room, By: who, Class: contract.ClassAuto, State: contract.OpApproved, Created: s.now(), Expires: s.now().Add(time.Hour)}, Args: json.RawMessage(`{}`)}
			if err := s.store.InsertOperation(tx, op, contract.Event{}, nil); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms,lost_at,stopped) VALUES(?,?,?,?,?,?,?,'lost')", id, grants[0].ID, "host", id, s.now().UnixMilli(), 600000, s.now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StopHostexecs(ctx); err != nil {
		t.Fatal(err)
	}
	<-f.second
	close(f.release)
	<-f.done
	<-f.done
}

type deadlineHostexec struct{}

func (deadlineHostexec) RunHostexec(ctx context.Context, _ contract.HostexecRunArgs, _ contract.OpID) ([]byte, int, bool, error) {
	<-ctx.Done()
	return nil, 0, false, ctx.Err()
}
func (deadlineHostexec) StopHostexec(context.Context, contract.HostName, string) error { return nil }

type expiredHostexecContext struct {
	context.Context
	done <-chan struct{}
}

func (c expiredHostexecContext) Done() <-chan struct{} { return c.done }
func (expiredHostexecContext) Err() error              { return context.DeadlineExceeded }
func TestHostexecDeadlineRecordsLost(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	s.SetHostexecRunner(deadlineHostexec{})
	var duration time.Duration
	s.hostexecTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		duration = d
		done := make(chan struct{})
		close(done)
		return expiredHostexecContext{Context: ctx, done: done}, func() {}
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, e := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}, Timeout: contract.DurationMS(time.Minute.Milliseconds())})
	if e != nil || res.Operation.Result == nil || !strings.Contains(res.Operation.Result.Error, context.DeadlineExceeded.Error()) || res.Operation.Result.Cause != contract.CauseHostLost || duration != 75*time.Second {
		t.Fatalf("result %+v error %+v deadline %s", res, e, duration)
	}
	var stopped string
	if err := s.store.Read().QueryRow("SELECT stopped FROM hostexec_runs WHERE op=?", res.Operation.ID).Scan(&stopped); err != nil || stopped != "lost" {
		t.Fatalf("stopped %q: %v", stopped, err)
	}
}

func TestHostexecCardEmptyCwdAndStoreError(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	grants, err := s.store.ListActiveGrants(ctx, room, who, s.now())
	if err != nil {
		t.Fatal(err)
	}
	args := contract.HostexecRunArgs{Room: room, Host: "host", Argv: []string{"true"}, Grant: grants[0].ID}
	entry := s.HostexecRunEntry()
	if body := entry.Card(ctx, args, who).Body; strings.Contains(body, "Working directory") {
		t.Fatalf("body %q", body)
	}
	inactive := args
	inactive.Grant = "missing-grant"
	result := entry.Execute(ctx, "missing-operation", inactive, who)
	if result.Cause != contract.CauseGrantInactive {
		t.Fatalf("inactive result %+v", result)
	}
	result = entry.Execute(ctx, "missing-operation", args, who)
	if result.Error == "" || result.Error == "grant no longer active" {
		t.Fatalf("result %+v", result)
	}
}
