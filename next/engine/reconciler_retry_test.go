package engine

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type retryHost struct {
	creates  int
	removes  int
	starts   int
	startErr error
}

func (*retryHost) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}
func (h *retryHost) Create(context.Context, lifecycle.BoxSpec) (string, error) {
	h.creates++
	return "image", nil
}
func (h *retryHost) Remove(context.Context, contract.BoxID) error { h.removes++; return nil }
func (h *retryHost) Start(context.Context, contract.BoxID) error  { h.starts++; return h.startErr }
func (*retryHost) Stop(context.Context, contract.BoxID) error     { return nil }
func (h *retryHost) Recreate(context.Context, lifecycle.BoxSpec, string, string) error {
	h.starts++
	return h.startErr
}

type failingCreateHost struct {
	fakeBoxes
	id      contract.BoxID
	creates int
	removes int
}

func (h *failingCreateHost) Create(context.Context, lifecycle.BoxSpec) (string, error) {
	h.creates++
	return "", errors.New("create unavailable")
}

func (h *failingCreateHost) Remove(_ context.Context, id contract.BoxID) error {
	if id == h.id {
		h.removes++
	}
	return nil
}

func TestReconcilerCreateRetriesBeforeFailure(t *testing.T) {
	var logs bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLog) })
	s, room, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating', image='', want='run', due_at=0, failures=0 WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	host := &failingCreateHost{id: session.Box}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, func() time.Time { return now })
	for i, delay := range []time.Duration{30 * time.Second, time.Minute, 0} {
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		rows, err := s.store.LifecycleBoxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, row := range rows {
			if row.ID != session.Box {
				continue
			}
			found = true
			want := contract.BoxPending
			if i == 2 {
				want = contract.BoxFailed
			}
			if i == 2 && row.Reason != string(contract.CauseCreateFailed) {
				t.Fatalf("terminal reason=%q", row.Reason)
			}
			if i < 2 && row.Reason != string(box.PendingCreateRetry) {
				t.Fatalf("attempt %d: persisted reason=%q, want %q", i+1, row.Reason, box.PendingCreateRetry)
			}
			if row.State != want || row.Failures != i+1 || (delay != 0 && !row.DueAt.Equal(now.Add(delay))) {
				t.Fatalf("attempt %d: state=%s failures=%d due=%s", i+1, row.State, row.Failures, row.DueAt)
			}
		}
		if !found || host.creates != i+1 || host.removes != i+1 {
			t.Fatalf("attempt %d: found=%v creates=%d removes=%d", i+1, found, host.creates, host.removes)
		}
		if delay != 0 {
			if err := r.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			r.Wait()
			if host.creates != i+1 {
				t.Fatal("create retried before deadline")
			}
			now = now.Add(delay)
		}
	}
	log.SetOutput(previousLog)
	if !strings.Contains(logs.String(), "create unavailable") || !strings.Contains(logs.String(), string(session.Box)) {
		t.Fatalf("create error missing from log: %q", logs.String())
	}
	page, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Limit: 1000, Types: []contract.EventType{"box.changed"}})
	if problem != nil {
		t.Fatal(problem)
	}
	var retries int
	var failed bool
	for _, event := range page.Events {
		change := event.Body.(contract.BoxChanged)
		if change.Box != session.Box {
			continue
		}
		if change.From == contract.BoxCreating && change.To == contract.BoxPending {
			if change.Reason != string(box.PendingCreateRetry) {
				t.Fatalf("owner event reason=%q, want %q", change.Reason, box.PendingCreateRetry)
			}
			retries++
		}
		if change.To == contract.BoxFailed && change.Cause == contract.CauseCreateFailed {
			if change.Reason != string(contract.CauseCreateFailed) {
				t.Fatalf("terminal event reason=%q", change.Reason)
			}
			failed = true
		}
	}
	if retries != 2 || !failed {
		t.Fatalf("owner events: retries=%d terminal failure=%v", retries, failed)
	}
}

func TestReconcilerCleansPinnedPendingCreate(t *testing.T) {
	s, _, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='pending', image='', want='run', due_at=0, failures=0 WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	host := &failingCreateHost{id: session.Box}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, s.now)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if host.creates != 1 || host.removes != 1 {
		t.Fatalf("pinned create: creates=%d removes=%d", host.creates, host.removes)
	}
}

type retryView struct{}

func (retryView) OpenTerminal(contract.SessionID) bool { return false }
func (retryView) SessionClosed(contract.SessionID)     {}
func (retryView) CompactBeforeStop(context.Context, contract.SessionID) bool {
	return false
}
func (retryView) Connected(contract.SessionID) bool { return false }

func TestReconcilerResumesInterruptedCreate(t *testing.T) {
	s, _, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating', image='', want='run', due_at=0 WHERE id=?", session.Box)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &retryHost{}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, s.now)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if host.removes != 1 || host.creates != 1 || b.State != contract.BoxStarting {
		t.Fatalf("interrupted create: removes=%d creates=%d state=%s", host.removes, host.creates, b.State)
	}
}

func TestReconcilerStartBackoff(t *testing.T) {
	s, room, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped', want='run', due_at=0, failures=0 WHERE id=?", session.Box)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	host := &retryHost{startErr: errors.New("host unavailable")}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, func() time.Time { return now })
	delays := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, delay := range delays {
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		rows, err := s.store.LifecycleBoxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, row := range rows {
			if row.ID != session.Box {
				continue
			}
			found = true
			if row.State != contract.BoxStopped || row.Failures != i+1 || !row.DueAt.Equal(now.Add(delay)) {
				t.Fatalf("failure %d: state=%s failures=%d due=%s, want %s", i+1, row.State, row.Failures, row.DueAt, now.Add(delay))
			}
		}
		if !found {
			t.Fatal("box missing")
		}
		now = now.Add(delay)
	}
	events, err := s.store.Events(ctx, room, 0, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, event := range events {
		if changed, ok := event.Body.(contract.BoxChanged); ok && changed.Box == session.Box && string(changed.Cause) == "start_failed" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("third failed start did not publish start_failed")
	}
}

type removeRetryHost struct {
	fakeBoxes
	calls int
}

func (h *removeRetryHost) Remove(context.Context, contract.BoxID) error {
	h.calls++
	if h.calls == 1 {
		return errors.New("host unavailable")
	}
	return nil
}

func TestReconcilerRemoveBackoff(t *testing.T) {
	s, room, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	host := &removeRetryHost{}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, s.now)
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	before, err := s.store.Events(ctx, room, 0, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host.calls != 1 {
		t.Fatalf("remove calls after failure: %d, want 1", host.calls)
	}
	now = now.Add(29 * time.Second)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	after, err := s.store.Events(ctx, room, 0, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host.calls != 1 || len(after) != len(before) {
		t.Fatalf("before retry deadline: remove calls=%d events=%d, want 1 call and %d events", host.calls, len(after), len(before))
	}
	now = now.Add(time.Second)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	current, err := s.store.Box(ctx, session.Box)
	if err != nil || host.calls != 2 || current.State != contract.BoxDeleted {
		t.Fatalf("after retry: calls=%d box=%+v err=%v", host.calls, current, err)
	}
}

func TestReconcilerArchiveStartFailureAndRetry(t *testing.T) {
	s, room, sid, ctx := setup(t)
	connectAgent(ctx, t, s, sid)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	host := &retryHost{startErr: errors.New("host unavailable")}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, s.now)
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	current, err := s.store.Box(ctx, session.Box)
	if err != nil || host.starts != 1 || current.State != contract.BoxClosing || current.Reason != "host unavailable" {
		t.Fatalf("failed archive start: starts=%d box=%+v err=%v", host.starts, current, err)
	}
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{"box.changed"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		change := event.Body.(contract.BoxChanged)
		if change.Box == session.Box && change.From == contract.BoxClosing && change.To == contract.BoxClosing && change.Reason == "host unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatal("archive start failure did not publish box.changed")
	}
	host.startErr = nil
	now = now.Add(30 * time.Second)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	current, err = s.store.Box(ctx, session.Box)
	if err != nil || host.starts != 2 || current.State != contract.BoxClosing || current.Reason != "" {
		t.Fatalf("successful archive retry: starts=%d box=%+v err=%v", host.starts, current, err)
	}
}

type pendingRetryHost struct {
	fakeBoxes
	service          *Service
	session          contract.SessionID
	creates, removes int
	hello            *contract.Error
}

func (h *pendingRetryHost) Remove(context.Context, contract.BoxID) error {
	h.removes++
	return nil
}

func (h *pendingRetryHost) Create(ctx context.Context, _ lifecycle.BoxSpec) (string, error) {
	h.creates++
	if h.creates == 1 {
		return "", errors.New("create failed")
	}
	_, h.hello = h.service.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: h.session}, contract.BoxHelloRequest{})
	return "image", nil
}

func TestPendingCreateRetry(t *testing.T) {
	for _, closeDuringBackoff := range []bool{false, true} {
		name := "retry reaches ready"
		if closeDuringBackoff {
			name = "close during backoff"
		}
		t.Run(name, func(t *testing.T) {
			s, _, sid, ctx := setup(t)
			session, err := s.store.Session(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating', image='', want='run', due_at=0, failures=0 WHERE id=?", session.Box)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			host := &pendingRetryHost{service: s, session: sid}
			r := lifecycle.New(s.store, host, retryView{}, nil, s.event, func() time.Time { return now })
			if err := r.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			r.Wait()
			rows, err := s.store.LifecycleBoxes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var pending bool
			for _, row := range rows {
				if row.ID == session.Box {
					pending = row.State == contract.BoxPending && row.Host == "host" && row.Failures == 1 && row.DueAt.Equal(now.Add(30*time.Second))
				}
			}
			if !pending {
				t.Fatalf("failed create did not retain its host in pending backoff: %+v", rows)
			}
			if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem == nil {
				t.Fatal("old box hello succeeded during pending backoff")
			}
			if closeDuringBackoff {
				if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid, Discard: true}); problem != nil {
					t.Fatal(problem)
				}
			} else {
				now = now.Add(30 * time.Second)
			}
			if err := r.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			r.Wait()
			b, err := s.store.Box(ctx, session.Box)
			if err != nil {
				t.Fatal(err)
			}
			if closeDuringBackoff {
				if host.creates != 1 || b.State != contract.BoxDeleted {
					t.Fatalf("closed pending box: creates=%d box=%+v", host.creates, b)
				}
			} else if host.creates != 2 || host.removes != 2 || host.hello != nil || b.State != contract.BoxReady {
				t.Fatalf("retry: creates=%d removes=%d hello=%v box=%+v", host.creates, host.removes, host.hello, b)
			}
		})
	}
}

type closeRetryCleanupHost struct {
	fakeBoxes
	service      *Service
	reconciler   *lifecycle.Reconciler
	session      contract.SessionID
	removes      int
	container    bool
	closeProblem *contract.Error
	passErr      error
}

func (h *closeRetryCleanupHost) Create(context.Context, lifecycle.BoxSpec) (string, error) {
	h.container = true // A failed create can leave a partial container.
	return "", errors.New("create failed")
}

func (h *closeRetryCleanupHost) Remove(ctx context.Context, _ contract.BoxID) error {
	h.removes++
	if h.removes == 2 {
		_, h.closeProblem = h.service.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: h.session, Discard: true})
		h.passErr = h.reconciler.Pass(ctx)
		return ctx.Err()
	}
	h.container = false
	return nil
}

func TestCloseDuringRetryCleanupRemovesContainer(t *testing.T) {
	s, _, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating', image='', want='run', due_at=0, failures=0 WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	host := &closeRetryCleanupHost{service: s, session: sid}
	r := lifecycle.New(s.store, host, retryView{}, nil, s.event, func() time.Time { return now })
	host.reconciler = r
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	now = now.Add(30 * time.Second)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxDeleted || host.container || host.closeProblem != nil || host.passErr != nil {
		t.Fatalf("close after retry cleanup: box=%+v container=%v close=%v pass=%v err=%v", b, host.container, host.closeProblem, host.passErr, err)
	}
}
