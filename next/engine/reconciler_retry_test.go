package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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

type retryView struct{}

func (retryView) OpenTerminal(contract.SessionID) bool { return false }
func (retryView) Connected(contract.SessionID) bool    { return false }

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
