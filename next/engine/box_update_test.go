package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
)

const oldBinary, newBinary = "old-sum", "new-sum"

// staleSession is a session whose last turn has just ended, under an engine on newBinary.
func staleSession(t *testing.T, jobs int) (*Service, *dormantHost, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, host, room, sid, ctx := quietSession(t, jobs)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	s.SetBinary(newBinary)
	return s, host, room, sid, ctx
}

// connectBox opens the box channel as an agent on binary and collects the turns it is given.
func connectBox(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID, binary string) (<-chan contract.TurnStart, func()) {
	t.Helper()
	channelCtx, cancel := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(channelCtx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, binary)
	if problem != nil {
		cancel()
		t.Fatal(problem)
	}
	turns := make(chan contract.TurnStart, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for message := range messages {
			if start, ok := message.Body.(contract.TurnStart); ok {
				turns <- start
			}
		}
	}()
	var stopped bool
	disconnect := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(disconnect)
	return turns, disconnect
}

func boxWant(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) string {
	t.Helper()
	var want string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT want FROM boxes WHERE session=?", sid).Scan(&want); err != nil {
		t.Fatal(err)
	}
	return want
}

func waitWant(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for boxWant(t, s, ctx, sid) != want {
		if time.Now().After(deadline) {
			t.Fatalf("box want=%s, waited for %s", boxWant(t, s, ctx, sid), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// updateNotices counts every delivery of the update notice, to anyone, and fails on one that could start a turn.
func updateNotices(t *testing.T, s *Service, ctx context.Context) int {
	t.Helper()
	var count, waking int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*),coalesce(sum(d.wake),0) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='notice.posted' AND json_extract(CAST(e.body AS TEXT),'$.text')=?", boxUpdateNotice).Scan(&count, &waking); err != nil {
		t.Fatal(err)
	}
	if waking != 0 {
		t.Fatalf("%d of %d update notices wake", waking, count)
	}
	return count
}

func turnCount(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) int {
	t.Helper()
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM turns WHERE session=?", sid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func openTurn(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID) contract.TurnID {
	t.Helper()
	wakeSession(t, s, ctx, room, sid)
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.store.Dispatch(ctx, sid, "turn_open", event, false)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestStaleBoxGetsOneNoticeAndNoTurn(t *testing.T) {
	s, _, _, sid, ctx := staleSession(t, 1) // the job keeps the box up
	turns := turnCount(t, s, ctx, sid)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	if got := updateNotices(t, s, ctx); got != 1 {
		t.Fatalf("notices=%d, want 1", got)
	}
	var home string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT home FROM events WHERE type='notice.posted'").Scan(&home); err != nil || home != string(sid) {
		t.Fatalf("notice home=%q: %v", home, err)
	}
	// The default subscription wakes root on a room's notice, so a subscription pass is what could start a turn here.
	if _, err := s.store.DispatchEvents(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if got := updateNotices(t, s, ctx); got != 1 {
		t.Fatalf("after dispatch: notices=%d, want 1", got)
	}
	passLifecycle(t, s, ctx)
	if got := turnCount(t, s, ctx, sid); got != turns {
		t.Fatalf("turns=%d, want %d", got, turns)
	}
	disconnect()
	_, disconnect = connectBox(t, s, ctx, sid, oldBinary)
	disconnect()
	if got := updateNotices(t, s, ctx); got != 1 {
		t.Fatalf("after reconnect: notices=%d, want 1", got)
	}
}

func TestEngineRestartSendsNoSecondNotice(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t, 1)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	disconnect()

	// Nothing in memory survives: this engine only has the store and the same binary.
	restarted := New(s.store, host, fakeProfiles{}, s.now)
	resetLifecycle(restarted)
	restarted.SetBinary(newBinary)
	_, disconnect = connectBox(t, restarted, ctx, sid, oldBinary)
	disconnect()
	if !staleFor(restarted, sid, oldBinary) {
		t.Fatal("the restarted engine did not relearn that the box is stale")
	}
	if got := updateNotices(t, restarted, ctx); got != 1 {
		t.Fatalf("stale box after restart: notices=%d, want 1", got)
	}

	// A new binary is news again, for a box still on the old one.
	restarted.SetBinary("newer-sum")
	_, disconnect = connectBox(t, restarted, ctx, sid, oldBinary)
	disconnect()
	if got := updateNotices(t, restarted, ctx); got != 2 {
		t.Fatalf("new binary: notices=%d, want 2", got)
	}
}

// staleFor connects again and reads the flag while the channel is up.
func staleFor(s *Service, sid contract.SessionID, binary string) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages, problem := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, binary)
	if problem != nil {
		return false
	}
	stale := s.Stale(sid)
	cancel()
	for range messages {
	}
	return stale
}

func TestCurrentBoxGetsNoNotice(t *testing.T) {
	for _, binary := range []string{newBinary, ""} { // "" is an agent from before boxes reported
		s, _, _, sid, ctx := staleSession(t, 0)
		_, disconnect := connectBox(t, s, ctx, sid, binary)
		if s.Stale(sid) {
			t.Fatalf("binary %q: stale", binary)
		}
		disconnect()
		if got := updateNotices(t, s, ctx); got != 0 {
			t.Fatalf("binary %q: notices=%d", binary, got)
		}
	}
}

func TestCurrentBoxKeepsTenMinuteRule(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t, 0)
	connectBox(t, s, ctx, sid, newBinary)
	s.now = func() time.Time { return time.Unix(1000, 0).Add(9 * time.Minute) }
	passLifecycle(t, s, ctx)
	if host.stops != 0 {
		t.Fatal("a current box stopped before ten quiet minutes")
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(11 * time.Minute) }
	passLifecycle(t, s, ctx)
	if host.stops != 1 {
		t.Fatalf("stops=%d after ten quiet minutes, want 1", host.stops)
	}
}

func TestStaleBoxNeverStopsMidTurnAndStopsWhenItEnds(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t, 0)
	turn := openTurn(t, s, ctx, room, sid)
	turns, _ := connectBox(t, s, ctx, sid, oldBinary)
	select {
	case <-turns:
	case <-time.After(5 * time.Second):
		t.Fatal("the open turn was not replayed")
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(time.Hour) }
	passLifecycle(t, s, ctx)
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
		t.Fatalf("stale box stopped mid-turn: stops=%d want=%s", host.stops, boxWant(t, s, ctx, sid))
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	// Nothing is pending, so it stays down until its next message, like any dormant box.
	passLifecycle(t, s, ctx)
	if host.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
}

func TestStaleBoxWaitsForJobs(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t, 1)
	connectBox(t, s, ctx, sid, oldBinary)
	s.now = func() time.Time { return time.Unix(1000, 0).Add(time.Hour) }
	passLifecycle(t, s, ctx)
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
		t.Fatalf("stale box stopped with a job running: stops=%d want=%s", host.stops, boxWant(t, s, ctx, sid))
	}
}

func openTerminal(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) *live.Session {
	t.Helper()
	handle, problem := s.createSession(ctx, sid)
	if problem != nil {
		t.Fatal(problem)
	}
	if !handle.OpenTerminal() {
		t.Fatal("terminal did not open")
	}
	return handle
}

func TestStaleBoxWaitsForTerminal(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t, 0)
	handle := openTerminal(t, s, ctx, sid)
	wakeSession(t, s, ctx, room, sid)
	turns, _ := connectBox(t, s, ctx, sid, oldBinary)
	var turn contract.TurnStart
	select {
	case turn = <-turns: // a terminal holds the update back, so the box keeps taking work
	case <-time.After(5 * time.Second):
		t.Fatal("no turn under a terminal")
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn.Turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	time.Sleep(100 * time.Millisecond) // the box loop sees the turn end, passes on the terminal and goes back to waiting
	passLifecycle(t, s, ctx)
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
		t.Fatalf("stale box stopped under a terminal: stops=%d want=%s", host.stops, boxWant(t, s, ctx, sid))
	}
	// Nothing wakes the box loop now, so only the reconciler can notice the terminal has gone.
	handle.CloseTerminal()
	passLifecycle(t, s, ctx)
	if host.stops != 1 {
		t.Fatalf("stops=%d after the terminal closed, want 1", host.stops)
	}
}

// A waiting delivery must not start another turn on the old binary, or a busy box would never update. It stays stored and
// starts the box, which is the only thing that turns it into a turn.
func TestPendingWakeRestartsStaleBoxWithDeliveryIntact(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	turns := turnCount(t, s, ctx, sid)
	started, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	select {
	case turn := <-started:
		t.Fatalf("a turn started on the old binary: %+v", turn)
	default:
	}
	if got := turnCount(t, s, ctx, sid); got != turns {
		t.Fatalf("turns=%d, want %d", got, turns)
	}
	var pending int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND wake=1 AND state='pending'", sid).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending wakes=%d: %v", pending, err)
	}

	// The next pass needs nothing from the stop that came before it, so a restarted engine does the same.
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting || boxWant(t, s, ctx, sid) != "run" {
		t.Fatalf("waiting wake did not start the box: starts=%d state=%s want=%s", host.starts, stateOf(t, s, ctx, sid), boxWant(t, s, ctx, sid))
	}
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, _ = connectBox(t, s, ctx, sid, newBinary)
	select {
	case turn := <-started:
		var text []string
		for _, input := range turn.Inputs {
			text = append(text, input.Text)
		}
		joined := strings.Join(text, "\n")
		if !strings.Contains(joined, "wake") || !strings.Contains(joined, boxUpdateNotice) {
			t.Fatalf("turn on the new binary lost an input: %q", joined)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no turn on the new binary")
	}
	waitWant(t, s, ctx, sid, "run")
	if s.Stale(sid) {
		t.Fatal("box on the new binary is still stale")
	}
}

type heldStopHost struct {
	dormantHost
	entered, release chan struct{}
}

func (h *heldStopHost) Stop(ctx context.Context, id contract.BoxID) error {
	close(h.entered)
	<-h.release
	return h.dormantHost.Stop(ctx, id)
}

// The wake puts want back to run while the box is still on its way down, and still connected.
func TestWakeDuringStaleStopStartsNoTurnOnDyingBox(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t, 0)
	host := &heldStopHost{entered: make(chan struct{}), release: make(chan struct{})}
	s.boxes = host
	resetLifecycle(s)
	turns := turnCount(t, s, ctx, sid)
	connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	<-host.entered
	wakeSession(t, s, ctx, room, sid)
	waitWant(t, s, ctx, sid, "stop")
	close(host.release)
	s.reconciler.Wait()
	if got := turnCount(t, s, ctx, sid); got != turns {
		t.Fatalf("turns=%d, want %d", got, turns)
	}
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("state=%s", stateOf(t, s, ctx, sid))
	}
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
}

// A terminal that opens after the stop was asked for keeps the box up, so the wake must reach the box loop.
func TestTerminalReversingStaleStopLetsWakeDispatch(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	started, _ := connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	openTerminal(t, s, ctx, sid)
	if err := s.store.WakeTerminal(ctx, session.Box); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting wake never dispatched")
	}
	if host.stops != 0 {
		t.Fatalf("stops=%d", host.stops)
	}
}

func TestPausedRoomHoldsWakeOfStoppedStaleBox(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room, Paused: true}); problem != nil {
		t.Fatal(problem)
	}
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	passLifecycle(t, s, ctx)
	if host.stops != 1 || host.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("paused: stops=%d starts=%d state=%s", host.stops, host.starts, stateOf(t, s, ctx, sid))
	}
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room, Paused: false}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatalf("resumed: starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
}

// An idle leaf told about an update may never run again, so the notice cannot hold its close or wake its parent.
func TestUpdateNoticeHoldsNoCloseAndWakesNoParent(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t, 0)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if _, problem := s.GoalDone(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.GoalDoneRequest{Room: room, Goal: "g1", Summary: "finished"}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000000, 0) }
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id=? AND seq NOT IN (SELECT seq FROM events WHERE type='notice.posted')", sid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET updated_at=0 WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", sid).Scan(&state); err != nil || state != "closing" {
		t.Fatalf("session=%s: %v", state, err)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: sid, Reason: contract.SessionCloseDormant})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, sid, event); err != nil {
		t.Fatal(err)
	}
	// Close drops the session's own deliveries, so any left here went to the parent.
	if got := updateNotices(t, s, ctx); got != 0 {
		t.Fatalf("notice was handed on: deliveries=%d", got)
	}
}
