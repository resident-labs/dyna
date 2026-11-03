package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
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

// A job the session stopped leaves what the session holds at once, with no wake and no notice, while another job stays.
func TestStoppedJobLeavesWhatTheSessionHolds(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	other := contract.ShellJobID("job_a1b2")
	endTurn(t, s, ctx, sid, turn, testJob, other)
	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob, End: &contract.ShellJobEnd{Kind: contract.ShellJobStopped}, Silent: true}); problem != nil {
		t.Fatal(problem)
	}
	held, err := s.store.Responsibilities(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []contract.ShellJobID
	for _, item := range held {
		if item.Kind == contract.ResponsibilityShellJob {
			jobs = append(jobs, contract.ShellJobID(item.ID))
		}
	}
	if len(jobs) != 1 || jobs[0] != other {
		t.Fatalf("held jobs = %v, want only %s", jobs, other)
	}
	var events int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type='shell.job.ended'").Scan(&events); err != nil || events != 0 {
		t.Fatalf("shell.job.ended events = %d, err %v", events, err)
	}
}
