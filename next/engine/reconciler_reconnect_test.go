package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type failingInventory struct {
	fakeBoxes
	calls int
}

func (h *failingInventory) Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error) {
	h.calls++
	return box.ContainerStatus{}, false, errors.New("inventory unavailable")
}

func TestConnectedLostInventoryErrorIsDelayed(t *testing.T) {
	s, _, sid, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	host := &failingInventory{}
	s.reconciler = lifecycle.New(s.store, host, s, nil, s.event, s.now)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET state='unreachable' WHERE name='host'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	channelCtx, cancel := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(channelCtx, caller, "")
	if problem != nil {
		cancel()
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range messages {
		}
	}()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET state='ready' WHERE name='host'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxLost || host.calls != 1 {
		t.Fatalf("box=%+v, inspections=%d: %v", b, host.calls, err)
	}
}

func TestReconnectAfterHostLossBeforePass(t *testing.T) {
	s, _, sid, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	setHost := func(state string) {
		t.Helper()
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE hosts SET state=? WHERE name='host'", state)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	pass := func(want contract.BoxState) {
		t.Helper()
		if err := s.Reconciler().Pass(ctx); err != nil {
			t.Fatal(err)
		}
		s.Reconciler().Wait()
		b, err := s.store.Box(ctx, session.Box)
		if err != nil || b.State != want {
			t.Fatalf("box state=%s, want %s: %v", b.State, want, err)
		}
	}
	setHost("unreachable")
	channelCtx, cancel := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(channelCtx, caller, "")
	if problem != nil {
		cancel()
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range messages {
		}
	}()
	pass(contract.BoxLost)
	setHost("ready")
	pass(contract.BoxReady)
}

func TestLostBoxChannelReconnect(t *testing.T) {
	for _, reconnectBeforeHost := range []bool{false, true} {
		name := "host-first"
		if reconnectBeforeHost {
			name = "channel-first"
		}
		t.Run(name, func(t *testing.T) {
			s, room, sid, ctx := setup(t)
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
			if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
				t.Fatal(problem)
			}
			session, err := s.store.Session(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			setHost := func(state string) {
				t.Helper()
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE hosts SET state=? WHERE name='host'", state)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			pass := func(want contract.BoxState) {
				t.Helper()
				if err := s.Reconciler().Pass(ctx); err != nil {
					t.Fatal(err)
				}
				s.Reconciler().Wait()
				b, err := s.store.Box(ctx, session.Box)
				if err != nil || b.State != want {
					t.Fatalf("box=%+v, want %s: %v", b, want, err)
				}
			}
			setHost("unreachable")
			pass(contract.BoxLost)
			if !reconnectBeforeHost {
				setHost("ready")
				// Inventory a running box while disconnected, setting its retry deadline.
				pass(contract.BoxLost)
			}
			channelCtx, cancel := context.WithCancel(ctx)
			messages, problem := s.BoxChannel(channelCtx, caller, "")
			if problem != nil {
				cancel()
				t.Fatal(problem)
			}
			defer func() {
				cancel()
				for range messages {
				}
			}()
			if reconnectBeforeHost {
				pass(contract.BoxLost)
				setHost("ready")
			}
			pass(contract.BoxReady)
			events, err := s.store.Events(ctx, room, 0, 256, []contract.EventType{"box.changed"})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				changed := event.Body.(contract.BoxChanged)
				if changed.Box == session.Box && changed.From == contract.BoxLost && changed.To == contract.BoxReady {
					found = true
				}
			}
			if !found {
				t.Fatal("lost-to-ready event missing")
			}
		})
	}
}

type stoppedContainerBoxes struct{ fakeBoxes }

func (stoppedContainerBoxes) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Box: id, Running: false}, true, nil
}

func TestFirstCrashRestartsAtOnceAndLaterCrashesBackOff(t *testing.T) {
	s, _, sid, ctx := setup(t)
	s.boxes = stoppedContainerBoxes{}
	s.reconciler = lifecycle.New(s.store, s.boxes, s, s.repos, s.event, func() time.Time { return s.now() })
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	crash := func() (state string, delay time.Duration, failures int) {
		t.Helper()
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='lost',reason='box disconnected',due_at=0 WHERE id=?", session.Box)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		passLifecycle(t, s, ctx)
		var due int64
		if err := s.store.Read().QueryRowContext(ctx, "SELECT state,due_at,failures FROM boxes WHERE id=?", session.Box).Scan(&state, &due, &failures); err != nil {
			t.Fatal(err)
		}
		if due != 0 {
			delay = time.UnixMilli(due).Sub(s.now())
		}
		return state, delay, failures
	}
	if state, delay, failures := crash(); state == "failed" || delay != 0 || failures != 1 {
		t.Fatalf("first crash: state %s, delay %v, failures %d", state, delay, failures)
	}
	if state, delay, failures := crash(); state == "failed" || delay != 60*time.Second || failures != 2 {
		t.Fatalf("second crash: state %s, delay %v, failures %d", state, delay, failures)
	}
	if state, _, failures := crash(); state != "failed" || failures != 3 {
		t.Fatalf("third crash: state %s, failures %d", state, failures)
	}
}
