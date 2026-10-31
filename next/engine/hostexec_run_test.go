package engine

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHostexec struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	started bool
	stopped bool
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
	return nil
}
func TestHostexecRevocationRetryCoversLateLaunch(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	runner := &fakeHostexec{entered: make(chan struct{}), release: make(chan struct{})}
	s.SetHostexecRunner(runner)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	grants, err := s.store.ListActiveGrants(ctx, room, principal, s.now())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
		done <- e
	}()
	<-runner.entered
	if _, e := s.HostexecRevoke(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostexecRevokeRequest{Grant: grants[0].ID}); e != nil {
		t.Fatal(e)
	}
	runner.mu.Lock()
	runner.started = true
	runner.mu.Unlock()
	if err := s.StopHostexecs(ctx); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	stopped := runner.stopped
	runner.mu.Unlock()
	close(runner.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if !stopped {
		t.Fatal("retry did not stop late launch")
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
	if err := s.store.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op=?", res.Operation.ID).Scan(&stopped, &ended); err != nil || stopped != "lost" || !ended.Valid {
		t.Fatalf("stopped %s ended %+v: %v", stopped, ended, err)
	}
}
func TestHostexecOutputFileRoomAccess(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetFilesDir(t.TempDir())
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

func TestHostexecApprovalAndRevokedRecheck(t *testing.T) {
	s, room, worker, ctx := setup(t)
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	grant := contract.Grant{ID: "grant_test", Kind: contract.GrantHostExec, Room: room, To: principal, Hosts: []contract.HostName{"host"}, Commands: []contract.CommandRule{{Program: "true"}}, Mode: contract.GrantModeApprove, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: s.now()}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error { return s.store.InsertGrant(tx, grant) }); err != nil {
		t.Fatal(err)
	}
	s.SetHostexecRunner(completedHostexec{})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	result, e := s.HostexecRun(ctx, principal, contract.HostexecRunRequest{Host: "host", Argv: []string{"true"}})
	if e != nil || result.Operation.Class != contract.ClassOwner || result.Operation.State != contract.OpPending {
		t.Fatalf("result %+v error %+v", result, e)
	}
	if _, e = s.HostexecRevoke(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostexecRevokeRequest{Grant: grant.ID}); e != nil {
		t.Fatal(e)
	}
	if err := s.HostexecRunEntry().Check(ctx, result.Operation.Args, principal); err == nil {
		t.Fatal("revoked grant passed recheck")
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
