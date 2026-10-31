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
	s.ReconcileDormancy(ctx, false, true)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false, true)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("woken box did not start")
	}
}

func TestDormancyKeepsClosingAncestor(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	if err := s.store.MarkClosing(ctx, sid); err != nil {
		t.Fatal(err)
	}
	s.ReconcileDormancy(ctx, false, true)
	var state string
	err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", sid).Scan(&state)
	if err != nil || state != "closing" || h.stops != 0 {
		t.Fatalf("closing ancestor changed: state=%s stops=%d err=%v", state, h.stops, err)
	}
}

func TestDormancyTransitionRechecksClosing(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "wake"}[wake], func(t *testing.T) {
			s, _, room, sid, ctx := quietSession(t, 0)
			if wake {
				s.ReconcileDormancy(ctx, false, true)
				wakeSession(t, s, ctx, room, sid)
			}
			session, err := s.store.Session(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.store.Box(ctx, session.Box)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.MarkClosing(ctx, sid); err != nil {
				t.Fatal(err)
			}
			event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
			if err != nil {
				t.Fatal(err)
			}
			var changed bool
			if wake {
				changed, err = s.store.Wake(ctx, b, contract.BoxStarting, "", event)
			} else {
				changed, err = s.store.StopIdle(ctx, b, event, s.now())
			}
			if err != nil || changed {
				t.Fatalf("closing transition changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestDormancyKeepsParentWithLiveChild(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	_, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: sid, Name: "child", Profile: "test", Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	s.ReconcileDormancy(ctx, false, true)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("parent with a live child stopped")
	}
}

func TestDormancyKeepsShellJobs(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 1)
	s.ReconcileDormancy(ctx, false, true)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box with shell jobs stopped")
	}
}

func TestWakeDuringQuietPeriodPreventsStop(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false, true)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box stopped with a pending wake")
	}
}

func TestWakeWithoutRoomWaitsPending(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false, true)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=0")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, sid)
	s.ReconcileDormancy(ctx, false, true)
	if h.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxPending {
		t.Fatal("box did not wait for capacity")
	}
}

func TestDormancyRetriesFailedStop(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	h.stopErr = errors.New("unavailable")
	s.ReconcileDormancy(ctx, false, true)
	h.stopErr = nil
	s.ReconcileDormancy(ctx, false, false)
	if h.stops != 1 {
		t.Fatal("event retried the stop")
	}
	s.ReconcileDormancy(ctx, false, true)
	if h.stops != 2 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed stop was not retried")
	}
}

func TestDormancyRetriesFailedStart(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false, true)
	wakeSession(t, s, ctx, room, sid)
	h.startErr = errors.New("unavailable")
	s.ReconcileDormancy(ctx, false, true)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed start did not return to stopped")
	}
	h.startErr = nil
	s.ReconcileDormancy(ctx, false, false)
	if h.starts != 1 {
		t.Fatal("event retried the failed start")
	}
	s.ReconcileDormancy(ctx, false, true)
	if h.starts != 2 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("failed start was not retried")
	}
}

func TestStoppedBoxRejectsHello(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	s.ReconcileDormancy(ctx, false, true)
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
	s.ReconcileDormancy(ctx, false, true)
	if h.starts != 0 {
		t.Fatal("started a box without a container")
	}
}

func TestWakeStartsDormantBudgetParent(t *testing.T) {
	s, room, parent, ctx := setup(t)
	host := &dormantHost{}
	s.boxes = host
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id IN (?,?)", parent, child.Session); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET state='dormant' WHERE id IN (?,?)", parent, child.Session); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped' WHERE session IN (?,?)", parent, child.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, child.Session)
	s.ReconcileDormancy(ctx, false, true)
	if host.starts != 2 || stateOf(t, s, ctx, parent) != contract.BoxStarting || stateOf(t, s, ctx, child.Session) != contract.BoxStarting {
		t.Fatalf("parent and child did not start: %d", host.starts)
	}
}
