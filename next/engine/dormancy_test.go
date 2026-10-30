package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type dormantHost struct {
	fakeBoxes
	starts, stops     int
	startErr, stopErr error
}

func (h *dormantHost) Start(context.Context, contract.BoxID) error { h.starts++; return h.startErr }
func (h *dormantHost) Stop(context.Context, contract.BoxID) error  { h.stops++; return h.stopErr }

func quietSession(t *testing.T, jobs int) (*Service, *dormantHost, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, sid, ctx := setup(t)
	host := &dormantHost{}
	s.boxes = host
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, err := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); err != nil {
		t.Fatal(err)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := s.store.Dispatch(ctx, sid, "turn_quiet", event, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, ShellJobs: jobs}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(11 * time.Minute) }
	return s, host, room, sid, ctx
}

func stateOf(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) contract.BoxState {
	t.Helper()
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	return b.State
}

func wakeSession(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID) {
	t.Helper()
	_, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: sid}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
}

func TestDormancyStopsAndWakes(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("woken box did not start")
	}
}

func TestDormancyKeepsShellJobs(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 1)
	s.ReconcileDormancy(ctx, false)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box with shell jobs stopped")
	}
}

func TestWakeDuringQuietPeriodPreventsStop(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box stopped with a pending wake")
	}
}

func TestWakeWithoutRoomWaitsPending(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=0")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false)
	if h.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxPending {
		t.Fatal("box did not wait for capacity")
	}
}

func TestDormancyRetriesFailedStop(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	h.stopErr = errors.New("unavailable")
	s.ReconcileDormancy(ctx, false)
	h.stopErr = nil
	s.ReconcileDormancy(ctx, false)
	if h.stops != 2 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed stop was not retried")
	}
}

func TestDormancyRetriesFailedStart(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false)
	wakeSession(t, s, ctx, room, sid)
	h.startErr = errors.New("unavailable")
	s.ReconcileDormancy(ctx, false)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed start did not return to stopped")
	}
	h.startErr = nil
	s.ReconcileDormancy(ctx, false)
	if h.starts != 2 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("failed start was not retried")
	}
}

func TestStoppedBoxRejectsHello(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false)
	_, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{})
	if problem == nil {
		t.Fatal("stopped box accepted hello")
	}
}

func TestDormancySkipsUncreatedPendingBox(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='pending',image='' WHERE id=?", session.Box)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, session.Room, sid)
	s.ReconcileDormancy(ctx, false)
	if h.starts != 0 {
		t.Fatal("started a box without a container")
	}
}
