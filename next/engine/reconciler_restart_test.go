package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// While the engine has not put a host's sockets back, its boxes cannot reconnect, so that silence must not count against them.
// The incident's boxes went lost exactly 60 seconds after each engine started, with their containers up.
func TestRestoringHostHoldsTheDisconnectClock(t *testing.T) {
	s, _, sid, ctx := setup(t)
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	host := &inspectHost{status: box.ContainerStatus{Running: true}, found: true}
	r := lifecycle.New(s.store, host, disconnectedView{}, nil, s.event, func() time.Time { return now })
	restoring := true
	r.SetHostRestoring(func(contract.HostName) bool { return restoring })
	pass := func(want contract.BoxState) {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		b, err := s.store.Box(ctx, session.Box)
		if err != nil || b.State != want {
			t.Fatalf("box=%+v, want %s: %v", b, want, err)
		}
	}
	pass(contract.BoxReady)
	now = now.Add(9 * time.Minute)
	pass(contract.BoxReady)
	restoring = false
	pass(contract.BoxReady)
	now = now.Add(59 * time.Second)
	pass(contract.BoxReady)
	now = now.Add(time.Second)
	pass(contract.BoxLost)
}

// A host that really goes away still shows: the hold covers only the engine's own silence.
func TestRestoringHostStillReportsAnUnreachableHost(t *testing.T) {
	s, _, sid, ctx := setup(t)
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET state='unreachable' WHERE name='host'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := lifecycle.New(s.store, &inspectHost{}, disconnectedView{}, nil, s.event, time.Now)
	r.SetHostRestoring(func(contract.HostName) bool { return true })
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxLost || b.Reason != "host unreachable" {
		t.Fatalf("box=%+v: %v", b, err)
	}
}

type countingRemoval struct {
	fakeBoxes
	stops, removals int
}

func (h *countingRemoval) Stop(context.Context, contract.BoxID) error {
	h.stops++
	return nil
}

func (h *countingRemoval) Remove(context.Context, contract.BoxID) error {
	h.removals++
	return nil
}

// A box that was starting when the engine restarted cannot say hello until its forward is back. A new session's box would
// otherwise be removed and fail "no hello within 60 s".
func TestRestoringHostHoldsTheHelloDeadline(t *testing.T) {
	s, _, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='starting',host='host',due_at=? WHERE id=?", now.Add(-time.Minute).UnixMilli(), session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	host := &countingRemoval{}
	r := lifecycle.New(s.store, host, disconnectedView{}, nil, s.event, func() time.Time { return now })
	restoring := true
	r.SetHostRestoring(func(contract.HostName) bool { return restoring })
	pass := func() {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
	}
	pass()
	if b, err := s.store.Box(ctx, session.Box); err != nil || b.State != contract.BoxStarting || host.removals != 0 || host.stops != 0 {
		t.Fatalf("while restoring: box=%+v removals=%d stops=%d: %v", b, host.removals, host.stops, err)
	}
	restoring = false
	pass()
	if host.removals == 0 && host.stops == 0 {
		t.Fatal("the deadline did not resume after the restore")
	}
}

// A turn runs on through a restart: the box goes lost, the agent reconnects, and the engine replays the same turn. Nothing
// ends it, starts another or counts a crash, and its result lands as usual.
func TestTurnSurvivesRestartAndLoss(t *testing.T) {
	s, room, sid, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	first, problem := s.BoxChannel(firstCtx, caller)
	if problem != nil {
		stopFirst()
		t.Fatal(problem)
	}
	start := (<-first).Body.(contract.TurnStart)

	// The engine stops, and the next one has a fresh reconciler that never saw this box connected.
	stopFirst()
	for range first {
	}
	eventuallyDisconnected(t, s, sid)
	now := time.Unix(5000, 0)
	host := &inspectHost{status: box.ContainerStatus{Running: true}, found: true}
	r := lifecycle.New(s.store, host, s, nil, s.event, func() time.Time { return now })
	s.reconciler = r
	pass := func(want contract.BoxState) {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
		b, err := s.store.Box(ctx, session.Box)
		if err != nil || b.State != want {
			t.Fatalf("box=%+v, want %s: %v", b, want, err)
		}
	}
	pass(contract.BoxReady)
	now = now.Add(time.Minute)
	pass(contract.BoxLost)
	now = now.Add(time.Minute)
	pass(contract.BoxLost)

	turnState := func() (open, total int) {
		t.Helper()
		if err := s.store.Read().QueryRowContext(ctx, "SELECT coalesce(sum(ended_at IS NULL),0),count(*) FROM turns WHERE session=?", sid).Scan(&open, &total); err != nil {
			t.Fatal(err)
		}
		return open, total
	}
	if open, total := turnState(); open != 1 || total != 1 {
		t.Fatalf("while lost: %d open of %d", open, total)
	}

	// The forward is back, so the box agent reconnects on its own.
	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()
	second, problem := s.BoxChannel(secondCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	pass(contract.BoxReady)
	select {
	case message := <-second:
		replay, ok := message.Body.(contract.TurnStart)
		if !ok || replay.Turn != start.Turn || len(replay.Inputs) != len(start.Inputs) {
			t.Fatalf("replay %+v of %+v", message.Body, start)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not replay the turn")
	}
	if open, total := turnState(); open != 1 || total != 1 {
		t.Fatalf("after reattach: %d open of %d", open, total)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}
	if open, total := turnState(); open != 0 || total != 1 {
		t.Fatalf("after output: %d open of %d", open, total)
	}
	var outcome string
	var failures int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT t.outcome,b.failures FROM turns t JOIN boxes b ON b.session=t.session WHERE t.id=?", start.Turn).Scan(&outcome, &failures); err != nil {
		t.Fatal(err)
	}
	if outcome != string(contract.TurnCompleted) || failures != 0 {
		t.Fatalf("outcome %s, failures %d", outcome, failures)
	}
	events, err := s.store.Events(ctx, room, 0, 256, []contract.EventType{"session.restarted"})
	if err != nil || len(events) != 0 {
		t.Fatalf("restart notices %+v: %v", events, err)
	}
}

func eventuallyDisconnected(t *testing.T, s *Service, id contract.SessionID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Connected(id) {
		if time.Now().After(deadline) {
			t.Fatal("the channel stayed connected")
		}
		time.Sleep(time.Millisecond)
	}
}
