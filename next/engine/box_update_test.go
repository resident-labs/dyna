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

const (
	oldBinary, newBinary = "old-sum", "new-sum"
	testJob              = contract.ShellJobID("job_7f3b")
	heldByJob            = "An update is ready for your box. It applies when job_7f3b ends. Your files and conversation are kept."
	loopSettles          = 100 * time.Millisecond // for a box loop that must do nothing: long enough that it would have
)

// staleSession is a session whose last turn has just ended, under an engine on newBinary.
func staleSession(t *testing.T) (*Service, *dormantHost, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, host, room, sid, ctx := quietSession(t, 0)
	s.now = func() time.Time { return time.Unix(1000, 0) }
	s.SetBinary(newBinary)
	return s, host, room, sid, ctx
}

func boxCaller(sid contract.SessionID) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: sid}
}

// connectBox opens the box channel as an agent on binary and collects the turns it is given.
func connectBox(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID, binary string) (<-chan contract.TurnStart, func()) {
	t.Helper()
	channelCtx, cancel := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(channelCtx, boxCaller(sid), binary)
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

// updateNotices counts the update notices posted, and fails on a delivery of one that could start a turn.
func updateNotices(t *testing.T, s *Service, ctx context.Context) int {
	t.Helper()
	var count, waking int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*),(SELECT count(*) FROM deliveries d JOIN events n ON n.seq=d.seq WHERE n.type='session.notice' AND d.wake=1) FROM events e WHERE e.type='session.notice'").Scan(&count, &waking); err != nil {
		t.Fatal(err)
	}
	if waking != 0 {
		t.Fatalf("%d notice deliveries wake", waking)
	}
	return count
}

func noticeDeliveries(t *testing.T, s *Service, ctx context.Context) int {
	t.Helper()
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='session.notice'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func waitNotices(t *testing.T, s *Service, ctx context.Context, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for updateNotices(t, s, ctx) != want {
		if time.Now().After(deadline) {
			t.Fatalf("notices=%d, waited for %d", updateNotices(t, s, ctx), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func noticeText(t *testing.T, s *Service, ctx context.Context) string {
	t.Helper()
	var text string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT json_extract(CAST(body AS TEXT),'$.text') FROM events WHERE type='session.notice' ORDER BY seq DESC LIMIT 1").Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}

func turnCount(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) int {
	t.Helper()
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM turns WHERE session=?", sid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func openTurn(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID, id contract.TurnID) contract.TurnID {
	t.Helper()
	wakeSession(t, s, ctx, room, sid)
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.store.Dispatch(ctx, sid, id, event, false)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func endTurn(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID, turn contract.TurnID, jobs ...contract.ShellJobID) {
	t.Helper()
	if _, problem := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, ShellJobs: len(jobs), Jobs: jobs}); problem != nil {
		t.Fatal(problem)
	}
}

// leaveJob ends a turn with testJob still running.
func leaveJob(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID) {
	t.Helper()
	endTurn(t, s, ctx, sid, openTurn(t, s, ctx, room, sid, "turn_job"), testJob)
}

func turnInputs(t *testing.T, turns <-chan contract.TurnStart) string {
	t.Helper()
	select {
	case turn := <-turns:
		var text []string
		for _, input := range turn.Inputs {
			text = append(text, input.Text)
		}
		return strings.Join(text, "\n")
	case <-time.After(5 * time.Second):
		t.Fatal("no turn started")
		return ""
	}
}

func TestBoxHeldByJobGetsOneNoticeAndNoTurn(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	turns := turnCount(t, s, ctx, sid)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitNotices(t, s, ctx, 1)
	if got := noticeText(t, s, ctx); got != heldByJob {
		t.Fatalf("notice=%q", got)
	}
	// The default subscription wakes root on engine notices, so a subscription pass is what could start a turn here.
	if _, err := s.store.DispatchEvents(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if got := noticeDeliveries(t, s, ctx); got != 1 {
		t.Fatalf("after dispatch: deliveries=%d, want 1", got)
	}
	updateNotices(t, s, ctx)
	passLifecycle(t, s, ctx)
	if got := turnCount(t, s, ctx, sid); got != turns {
		t.Fatalf("turns=%d, want %d", got, turns)
	}
	if boxWant(t, s, ctx, sid) != "run" {
		t.Fatal("a box held by a job stopped")
	}
	disconnect()
	_, disconnect = connectBox(t, s, ctx, sid, oldBinary)
	time.Sleep(loopSettles)
	disconnect()
	if got := updateNotices(t, s, ctx); got != 1 {
		t.Fatalf("after reconnect: notices=%d, want 1", got)
	}
}

func TestBoxWithNothingHoldingItStopsWithNoNotice(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t)
	connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	if got := updateNotices(t, s, ctx); got != 0 {
		t.Fatalf("notices=%d, want 0", got)
	}
}

func TestEngineRestartSendsNoSecondNotice(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitNotices(t, s, ctx, 1)
	disconnect()

	// Nothing in memory survives: this engine only has the store and the same binary.
	restarted := New(s.store, host, fakeProfiles{}, s.now)
	resetLifecycle(restarted)
	restarted.SetBinary(newBinary)
	_, disconnect = connectBox(t, restarted, ctx, sid, oldBinary)
	if !restarted.Stale(sid) {
		t.Fatal("the restarted engine did not relearn that the box is stale")
	}
	time.Sleep(loopSettles)
	disconnect()
	if got := updateNotices(t, restarted, ctx); got != 1 {
		t.Fatalf("stale box after restart: notices=%d, want 1", got)
	}

	// A new binary is news again, for a box still on the old one.
	restarted.SetBinary("newer-sum")
	connectBox(t, restarted, ctx, sid, oldBinary)
	waitNotices(t, restarted, ctx, 2)
}

func TestCurrentBoxGetsNoNotice(t *testing.T) {
	for _, binary := range []string{newBinary, ""} { // "" is an agent from before boxes reported
		s, _, room, sid, ctx := staleSession(t)
		leaveJob(t, s, ctx, room, sid)
		_, disconnect := connectBox(t, s, ctx, sid, binary)
		if s.Stale(sid) {
			t.Fatalf("binary %q: stale", binary)
		}
		time.Sleep(loopSettles)
		disconnect()
		if got := updateNotices(t, s, ctx); got != 0 {
			t.Fatalf("binary %q: notices=%d", binary, got)
		}
	}
}

func TestCurrentBoxKeepsTenMinuteRule(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t)
	connectBox(t, s, ctx, sid, newBinary)
	s.now = func() time.Time { return time.Unix(1000, 0).Add(9 * time.Minute) }
	passLifecycle(t, s, ctx)
	if host.stops != 0 {
		t.Fatal("a current box stopped before ten quiet minutes")
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(11 * time.Minute) }
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if boxWant(t, s, ctx, sid) != "stop" {
		t.Fatalf("want=%s after ten quiet minutes", boxWant(t, s, ctx, sid))
	}
}

// A turn alone sends nothing: the box stops as it ends, so no later turn on this binary could read a notice.
func TestStaleBoxStopsWhenTurnEndsWithNoNotice(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, _ := connectBox(t, s, ctx, sid, oldBinary)
	turnInputs(t, turns)
	s.now = func() time.Time { return time.Unix(1000, 0).Add(time.Hour) }
	passLifecycle(t, s, ctx)
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
		t.Fatalf("stale box stopped mid-turn: stops=%d want=%s", host.stops, boxWant(t, s, ctx, sid))
	}
	endTurn(t, s, ctx, sid, turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	if got := updateNotices(t, s, ctx); got != 0 {
		t.Fatalf("notices=%d, want 0", got)
	}
	// Nothing is pending, so it stays down until its next message, like any dormant box.
	passLifecycle(t, s, ctx)
	if host.starts != 0 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
}

func TestOpenTurnAndJobAreBothNamed(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	openTurn(t, s, ctx, room, sid, "turn_open")
	connectBox(t, s, ctx, sid, oldBinary)
	waitNotices(t, s, ctx, 1)
	want := "An update is ready for your box. It applies when job_7f3b and your open turn end. Your files and conversation are kept."
	if got := noticeText(t, s, ctx); got != want {
		t.Fatalf("notice=%q", got)
	}
}

// The job holds the box, so a turn in the meantime reads the notice on the old binary, where it is true.
func TestHeldBoxReadsNoticeWithItsNextTurn(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	turns, _ := connectBox(t, s, ctx, sid, oldBinary)
	waitNotices(t, s, ctx, 1)
	if s.Stale(sid) != true || boxWant(t, s, ctx, sid) != "run" {
		t.Fatal("the job did not hold the box")
	}
	wakeSession(t, s, ctx, room, sid)
	if text := turnInputs(t, turns); !strings.Contains(text, heldByJob) || !strings.Contains(text, "wake") {
		t.Fatalf("turn inputs=%q", text)
	}
}

// The whole of the usual case: a turn leaves a job, the job ends with nothing read, and the box comes back on the new binary
// for the job's result, with no stale notice saying an update is coming.
func TestJobHoldsBoxThenEndingItAppliesUpdate(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	turnInputs(t, turns)
	if got := updateNotices(t, s, ctx); got != 0 {
		t.Fatalf("notices=%d during a turn with no job", got)
	}
	endTurn(t, s, ctx, sid, turn, testJob)
	waitNotices(t, s, ctx, 1)
	if got := noticeText(t, s, ctx); got != heldByJob {
		t.Fatalf("notice=%q", got)
	}
	passLifecycle(t, s, ctx)
	if host.stops != 0 {
		t.Fatal("stale box stopped with a job running")
	}
	// A job that ends just after the box loop passed the check starts one more turn on the old binary, and the box stops as
	// that turn ends. Nothing is lost, but it is not the path under test here.
	time.Sleep(loopSettles)
	if _, problem := s.BoxNotice(ctx, boxCaller(sid), contract.BoxNoticeRequest{Text: "package failed"}); problem != nil {
		t.Fatal(problem)
	}

	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob, End: jobExited, Tail: "built"}); problem != nil {
		t.Fatal(problem)
	}
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	if got := noticeDeliveries(t, s, ctx); got != 1 {
		t.Fatalf("deliveries=%d, want only the add-on notice", got)
	}
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	turns, _ = connectBox(t, s, ctx, sid, newBinary)
	if text := turnInputs(t, turns); !strings.Contains(text, "built") || !strings.Contains(text, "package failed") || strings.Contains(text, "An update is ready") {
		t.Fatalf("turn inputs=%q", text)
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
	s, host, room, sid, ctx := staleSession(t)
	handle := openTerminal(t, s, ctx, sid)
	wakeSession(t, s, ctx, room, sid)
	turns, _ := connectBox(t, s, ctx, sid, oldBinary)
	select {
	case start := <-turns: // a terminal holds the update back, so the box keeps taking work
		endTurn(t, s, ctx, sid, start.Turn)
	case <-time.After(5 * time.Second):
		t.Fatal("no turn under a terminal")
	}
	want := "An update is ready for your box. It applies when your open terminal ends. Your files and conversation are kept."
	if got := noticeText(t, s, ctx); got != want {
		t.Fatalf("notice=%q", got)
	}
	time.Sleep(loopSettles) // the box loop sees the turn end, passes on the terminal and goes back to waiting
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
	s, host, room, sid, ctx := staleSession(t)
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
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, _ = connectBox(t, s, ctx, sid, newBinary)
	if text := turnInputs(t, started); !strings.Contains(text, "wake") {
		t.Fatalf("turn on the new binary lost its input: %q", text)
	}
	if s.Stale(sid) || updateNotices(t, s, ctx) != 0 {
		t.Fatalf("after the update: stale=%v notices=%d", s.Stale(sid), updateNotices(t, s, ctx))
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
	s, _, room, sid, ctx := staleSession(t)
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
	s, host, room, sid, ctx := staleSession(t)
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
	turnInputs(t, started)
	if host.stops != 0 {
		t.Fatalf("stops=%d", host.stops)
	}
}

func TestPausedRoomHoldsWakeOfStoppedStaleBox(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
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

// A box that failed while held keeps its unread notice, and the leaf may never run again.
func TestUpdateNoticeHoldsNoCloseAndWakesNoParent(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitNotices(t, s, ctx, 1)
	disconnect()
	if _, problem := s.GoalDone(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.GoalDoneRequest{Room: room, Goal: "g1", Summary: "finished"}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000000, 0) }
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id=? AND seq NOT IN (SELECT seq FROM events WHERE type='session.notice')", sid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=0 WHERE id=?", sid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='failed',updated_at=0 WHERE session=?", sid)
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
	if got := noticeDeliveries(t, s, ctx); got != 0 {
		t.Fatalf("notice was handed on: deliveries=%d", got)
	}
}
