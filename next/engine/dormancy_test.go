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
	removed           []contract.BoxID
	startErr, stopErr error
}

func (h *dormantHost) Start(context.Context, contract.BoxID) error { h.starts++; return h.startErr }
func (h *dormantHost) Remove(_ context.Context, id contract.BoxID) error {
	h.removed = append(h.removed, id)
	return nil
}
func (h *dormantHost) Stop(context.Context, contract.BoxID) error { h.stops++; return h.stopErr }

func quietSession(t *testing.T, jobs int) (*Service, *dormantHost, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, sid, ctx := setup(t)
	host := &dormantHost{}
	s.boxes = host
	resetLifecycle(s)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, err := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); err != nil {
		t.Fatal(err)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.store.Dispatch(ctx, sid, "turn_quiet", event, false)
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
	passLifecycle(t, s, ctx)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("woken box did not start")
	}
}

func TestDormancyKeepsClosingAncestor(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	if err := s.store.MarkClosing(ctx, sid, false); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	var state string
	err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", sid).Scan(&state)
	if err != nil || state != "closing" || h.stops != 0 {
		t.Fatalf("closing ancestor changed: state=%s stops=%d err=%v", state, h.stops, err)
	}
}

func TestDormancyStopsParentWithLiveChild(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	_, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: sid, Name: "child", Profile: "test", Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("parent with a live child did not stop")
	}
}

func TestDormancyKeepsShellJobs(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 1)
	passLifecycle(t, s, ctx)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box with shell jobs stopped")
	}
}

func TestWakeDuringQuietPeriodPreventsStop(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.stops != 0 || stateOf(t, s, ctx, sid) != contract.BoxReady {
		t.Fatal("box stopped with a pending wake")
	}
}

func TestPausedWakeAllowsDormancyAndResumes(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room, Paused: true}); problem != nil {
		t.Fatal(problem)
	}
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("paused wake kept box running")
	}
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("resume did not wake dormant box")
	}
}

func TestWakeWithoutRoomWaitsPending(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=0")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxPending {
		t.Fatal("box did not wait for capacity")
	}
}

func TestDormancyRetriesFailedStop(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	// This test retries a stop on a connected box, not a lost box.
	attachTestChannel(t, s, sid)
	h.stopErr = errors.New("unavailable")
	passLifecycle(t, s, ctx)
	h.stopErr = nil
	passLifecycle(t, s, ctx)
	if h.stops != 1 {
		t.Fatal("event retried the stop")
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(12 * time.Minute) }
	passLifecycle(t, s, ctx)
	if h.stops != 2 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed stop was not retried")
	}
}

func TestDormancyRetriesFailedStart(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	wakeSession(t, s, ctx, room, sid)
	h.startErr = errors.New("unavailable")
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("failed start did not return to stopped")
	}
	h.startErr = nil
	passLifecycle(t, s, ctx)
	if h.starts != 1 {
		t.Fatal("event retried the failed start")
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(12 * time.Minute) }
	passLifecycle(t, s, ctx)
	if h.starts != 2 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("failed start was not retried")
	}
}

func TestStoppedBoxRejectsHello(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
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
	passLifecycle(t, s, ctx)
	if h.starts != 0 {
		t.Fatal("started a box without a container")
	}
}

func TestWakeDoesNotStartDormantParent(t *testing.T) {
	s, room, parent, ctx := setup(t)
	host := &dormantHost{}
	s.boxes = host
	resetLifecycle(s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id IN (?,?)", parent, child.Session); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped',want='stop',due_at=0 WHERE session IN (?,?)", parent, child.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wakeSession(t, s, ctx, room, child.Session)
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, parent) != contract.BoxStopped || stateOf(t, s, ctx, child.Session) != contract.BoxStarting {
		t.Fatalf("unexpected wake: %d parent=%s child=%s", host.starts, stateOf(t, s, ctx, parent), stateOf(t, s, ctx, child.Session))
	}
}

func listedJobs(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID) int {
	t.Helper()
	list, problem := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, session := range list.Sessions {
		if session.ID == sid {
			return session.ShellJobs
		}
	}
	t.Fatal("session not listed")
	return 0
}

func TestSessionListKeepsJobCountWhileATurnRuns(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	wakeSession(t, s, ctx, room, sid)
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.store.Dispatch(ctx, sid, "turn_two", event, false)
	if err != nil || turn != "turn_two" {
		t.Fatalf("turn %q: %v", turn, err)
	}
	// A job that starts and ends inside this turn must not lower the count of the job that still runs.
	if _, problem := s.ShellJobEnded(ctx, caller, contract.ShellJobEndedRequest{Job: "job_inside"}); problem != nil {
		t.Fatal(problem)
	}
	if got := listedJobs(t, s, ctx, room, sid); got != 1 {
		t.Fatalf("jobs during the turn: %d", got)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, ShellJobs: 1}); problem != nil {
		t.Fatal(problem)
	}
	if got := listedJobs(t, s, ctx, room, sid); got != 1 {
		t.Fatalf("jobs after the turn: %d", got)
	}
}

func TestSessionListLowersJobCountForAJobEndingInTheTurnEndMillisecond(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	s.now = func() time.Time { return time.Unix(1000, 0) } // the same millisecond as the turn end
	if _, problem := s.ShellJobEnded(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.ShellJobEndedRequest{Job: "job_1"}); problem != nil {
		t.Fatal(problem)
	}
	if got := listedJobs(t, s, ctx, room, sid); got != 0 {
		t.Fatalf("jobs after the job ended: %d", got)
	}
}

func TestSessionListShowsBoxReason(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='pending',reason='room-host' WHERE session=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	list, problem := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, session := range list.Sessions {
		if session.ID == worker && session.BoxReason == "room-host" {
			return
		}
	}
	t.Fatalf("room-host reason missing: %+v", list.Sessions)
}

func TestRoomListCountsFailedSessionsAndSessionListShowsTimes(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t1',?,1,5000,'failed','',0,0,0,0),('t2',?,6000,7000,'stopped','',0,0,0,0)", worker, worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rooms, problem := s.RoomList(ctx, owner, contract.RoomListRequest{})
	if problem != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].Failed != 1 {
		t.Fatalf("rooms %+v: %v", rooms, problem)
	}
	list, problem := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, session := range list.Sessions {
		if session.ID == worker {
			if !session.StartedAt.Equal(time.Unix(1000, 0)) || !session.LastTurnAt.Equal(time.UnixMilli(7000)) {
				t.Fatalf("times %v %v", session.StartedAt, session.LastTurnAt)
			}
		} else if !session.LastTurnAt.IsZero() {
			t.Fatalf("a session with no ended turn has lastTurnAt %v", session.LastTurnAt)
		}
	}
}

func TestSessionListHadJobsStaysAfterTheJobEnds(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	hadJobs := func() bool {
		list, problem := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, session := range list.Sessions {
			if session.ID == sid {
				return session.HadJobs
			}
		}
		t.Fatal("session not listed")
		return false
	}
	if hadJobs() {
		t.Fatal("hadJobs before any job ended")
	}
	if _, problem := s.ShellJobEnded(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.ShellJobEndedRequest{Job: "job_1"}); problem != nil {
		t.Fatal(problem)
	}
	if !hadJobs() {
		t.Fatal("hadJobs is false after a job ended")
	}
}

func TestRoomListCountsRoomHostWaits(t *testing.T) {
	s, _, worker, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='pending', reason=CASE WHEN session=? THEN 'room-host' ELSE 'room-limit' END", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rooms, problem := s.RoomList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomListRequest{})
	if problem != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].Hostless != 1 {
		t.Fatalf("rooms %+v: %v", rooms, problem)
	}
}

func TestRoomListCountsAFailedBoxOnceWithoutAFailedTurn(t *testing.T) {
	s, _, worker, ctx := setup(t)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='failed' WHERE session=?", worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rooms, problem := s.RoomList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomListRequest{})
	if problem != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].Failed != 1 {
		t.Fatalf("rooms %+v: %v", rooms, problem)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t1',?,1,5000,'failed','',0,0,0,0)", worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rooms, problem = s.RoomList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomListRequest{}); problem != nil || rooms.Rooms[0].Failed != 1 {
		t.Fatalf("a failed box and a failed turn count twice: %+v: %v", rooms, problem)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM turns WHERE session=?", worker); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE boxes SET state='lost' WHERE session=?", worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rooms, problem = s.RoomList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomListRequest{}); problem != nil || rooms.Rooms[0].Failed != 1 {
		t.Fatalf("a lost box with no failed turn counts once: %+v: %v", rooms, problem)
	}
}
