package engine

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var refreshOwner = contract.Principal{Kind: contract.PrincipalOwner}

func refreshBox(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) {
	t.Helper()
	response, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Session: sid})
	if problem != nil {
		t.Fatal(problem)
	}
	if !slices.Equal(response.Marked, []contract.SessionID{sid}) {
		t.Fatalf("marked=%v, want %s", response.Marked, sid)
	}
}

func startChild(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, name string, parent contract.SessionID) contract.SessionID {
	t.Helper()
	child, problem := s.SessionStart(ctx, refreshOwner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(child.Session), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	return child.Session
}

// An agent from before #1934 reports no binary, and a current one reports the engine's: neither is ever flagged on its own.
func TestRefreshStopsBoxWhenTurnEndsAndKeepsIt(t *testing.T) {
	for _, binary := range []string{"", newBinary} {
		s, host, room, sid, ctx := staleSession(t)
		removed := len(host.removed)
		turn := openTurn(t, s, ctx, room, sid, "turn_open")
		turns, disconnect := connectBox(t, s, ctx, sid, binary)
		turnInputs(t, turns)
		refreshBox(t, s, ctx, sid)
		if !s.Stale(sid) {
			t.Fatalf("binary %q: not flagged", binary)
		}
		time.Sleep(loopSettles)
		passLifecycle(t, s, ctx)
		if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
			t.Fatalf("binary %q: stopped mid-turn: stops=%d want=%s", binary, host.stops, boxWant(t, s, ctx, sid))
		}
		endTurn(t, s, ctx, sid, turn)
		waitWant(t, s, ctx, sid, "stop")
		passLifecycle(t, s, ctx)
		disconnect()
		if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped || len(host.removed) != removed {
			t.Fatalf("binary %q: stops=%d state=%s removed=%v", binary, host.stops, stateOf(t, s, ctx, sid), host.removed)
		}
		if got := updateNotices(t, s, ctx); got != 0 {
			t.Fatalf("binary %q: notices=%d", binary, got)
		}
		// Nothing is waiting, so it stays down until a wake, and the box that comes back is not marked.
		passLifecycle(t, s, ctx)
		if host.starts != 0 {
			t.Fatalf("binary %q: started with nothing waiting", binary)
		}
		wakeSession(t, s, ctx, room, sid)
		passLifecycle(t, s, ctx)
		if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
			t.Fatalf("binary %q: starts=%d state=%s", binary, host.starts, stateOf(t, s, ctx, sid))
		}
		if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
		turns, _ = connectBox(t, s, ctx, sid, newBinary)
		if text := turnInputs(t, turns); !strings.Contains(text, "wake") {
			t.Fatalf("binary %q: turn lost its input: %q", binary, text)
		}
		if s.Stale(sid) || boxWant(t, s, ctx, sid) != "run" {
			t.Fatalf("binary %q: came back marked: stale=%v want=%s", binary, s.Stale(sid), boxWant(t, s, ctx, sid))
		}
	}
}

func TestRefreshHeldByJobGetsOneNoticePerMark(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	turns := turnCount(t, s, ctx, sid)
	_, disconnect := connectBox(t, s, ctx, sid, newBinary)
	refreshBox(t, s, ctx, sid)
	waitNotices(t, s, ctx, 1)
	if got := noticeText(t, s, ctx); got != heldByJob {
		t.Fatalf("notice=%q", got)
	}
	refreshBox(t, s, ctx, sid) // a repeat is the same mark
	time.Sleep(loopSettles)
	passLifecycle(t, s, ctx)
	if got := updateNotices(t, s, ctx); got != 1 {
		t.Fatalf("after a repeat: notices=%d", got)
	}
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" || turnCount(t, s, ctx, sid) != turns {
		t.Fatalf("a job did not hold the box: stops=%d want=%s turns=%d", host.stops, boxWant(t, s, ctx, sid), turnCount(t, s, ctx, sid))
	}

	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob, Tail: "built"}); problem != nil {
		t.Fatal(problem)
	}
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	if got := noticeDeliveries(t, s, ctx); got != 0 {
		t.Fatalf("unread notice kept: deliveries=%d", got)
	}
	passLifecycle(t, s, ctx) // the job's result is the wake
	if host.starts != 1 {
		t.Fatalf("starts=%d", host.starts)
	}
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, _ := connectBox(t, s, ctx, sid, newBinary)
	select {
	case start := <-started:
		if text := start.Inputs[0].Text; !strings.Contains(text, "built") {
			t.Fatalf("turn input=%q", text)
		}
		endTurn(t, s, ctx, sid, start.Turn, "job_next")
	case <-time.After(5 * time.Second):
		t.Fatal("no turn started")
	}
	// A second mark on the same binary is news, so the notice ID is not the binary's.
	refreshBox(t, s, ctx, sid)
	waitNotices(t, s, ctx, 2)
}

func TestRefreshOutlastsReconnect(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	_, disconnect := connectBox(t, s, ctx, sid, newBinary)
	refreshBox(t, s, ctx, sid)
	waitNotices(t, s, ctx, 1)
	disconnect()
	connectBox(t, s, ctx, sid, newBinary)
	if !s.Stale(sid) {
		t.Fatal("a reconnect lost the mark")
	}
	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob}); problem != nil {
		t.Fatal(problem)
	}
	waitWant(t, s, ctx, sid, "stop")
}

// A box agent says hello once, as it starts, so an engine restart that a box only reconnects through does not clear a mark. The
// mark is in memory, like the stale flag, but a box reports no mark to rebuild it from.
func TestEngineRestartForgetsRefresh(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	_, disconnect := connectBox(t, s, ctx, sid, newBinary)
	refreshBox(t, s, ctx, sid)
	waitNotices(t, s, ctx, 1)
	disconnect()
	restarted := New(s.store, host, fakeProfiles{}, s.now)
	resetLifecycle(restarted)
	restarted.SetBinary(newBinary)
	connectBox(t, restarted, ctx, sid, newBinary)
	if restarted.Stale(sid) {
		t.Fatal("the mark survived an engine restart")
	}
}

func TestRefreshRoomMarksEveryRunningBox(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	child := startChild(t, s, ctx, room, "child", sid)
	down := startChild(t, s, ctx, room, "down", sid)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET want='stop',state='stopped' WHERE session=?", down)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stops := host.stops
	connectBox(t, s, ctx, sid, newBinary)
	connectBox(t, s, ctx, child, newBinary)
	response, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	slices.Sort(response.Marked)
	want := []contract.SessionID{sid, child}
	slices.Sort(want)
	if !slices.Equal(response.Marked, want) {
		t.Fatalf("marked=%v, want %v", response.Marked, want)
	}
	waitWant(t, s, ctx, sid, "stop")
	waitWant(t, s, ctx, child, "stop")
	passLifecycle(t, s, ctx)
	if host.stops != stops+2 || stateOf(t, s, ctx, sid) != contract.BoxStopped || stateOf(t, s, ctx, child) != contract.BoxStopped {
		t.Fatalf("stops=%d, want %d: %s %s", host.stops, stops+2, stateOf(t, s, ctx, sid), stateOf(t, s, ctx, child))
	}
}

func TestRefreshLeavesStoppedBoxAlone(t *testing.T) {
	s, host, room, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	removed := len(host.removed)
	for _, req := range []contract.SessionRefreshRequest{{Session: sid}, {Room: room}} {
		response, problem := s.SessionRefresh(ctx, refreshOwner, req)
		if problem != nil || len(response.Marked) != 0 {
			t.Fatalf("%+v: marked=%v: %v", req, response.Marked, problem)
		}
	}
	passLifecycle(t, s, ctx)
	if host.starts != 0 || host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped || len(host.removed) != removed {
		t.Fatalf("starts=%d stops=%d state=%s removed=%v", host.starts, host.stops, stateOf(t, s, ctx, sid), host.removed)
	}
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	turns, _ := connectBox(t, s, ctx, sid, newBinary)
	turnInputs(t, turns)
	if s.Stale(sid) {
		t.Fatal("a stopped box came back marked")
	}
}

func TestRefreshAccess(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	child := startChild(t, s, ctx, room, "child", sid)
	peer := startChild(t, s, ctx, room, "peer", "")
	other, problem := s.RoomCreate(ctx, refreshOwner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	single := contract.SessionRefreshRequest{Session: child}
	if _, problem := s.SessionRefresh(ctx, boxCaller(peer), contract.SessionRefreshRequest{Session: sid}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("sibling: %v", problem)
	}
	for _, caller := range []contract.Principal{boxCaller(peer), {Kind: contract.PrincipalEngine}} {
		if _, problem := s.SessionRefresh(ctx, caller, single); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("caller %+v: %v", caller, problem)
		}
	}
	if s.Stale(child) {
		t.Fatal("a refused caller marked the box")
	}
	for caller, req := range map[string]contract.SessionRefreshRequest{"neither": {}, "both": {Session: child, Room: room}} {
		if _, problem := s.SessionRefresh(ctx, refreshOwner, req); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("%s: %v", caller, problem)
		}
	}
	if _, problem := s.SessionRefresh(ctx, boxCaller(sid), contract.SessionRefreshRequest{Room: other.Room}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("other room: %v", problem)
	}
	if _, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Room: "room_none"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown room: %v", problem)
	}
	// In a room, a session reaches only what is below it, and not itself.
	connectBox(t, s, ctx, sid, newBinary)
	connectBox(t, s, ctx, child, newBinary)
	connectBox(t, s, ctx, peer, newBinary)
	response, problem := s.SessionRefresh(ctx, boxCaller(child), contract.SessionRefreshRequest{Room: room})
	if problem != nil || len(response.Marked) != 0 {
		t.Fatalf("leaf: marked=%v: %v", response.Marked, problem)
	}
	response, problem = s.SessionRefresh(ctx, boxCaller(sid), contract.SessionRefreshRequest{Room: room})
	if problem != nil || !slices.Equal(response.Marked, []contract.SessionID{child}) {
		t.Fatalf("lead: marked=%v: %v", response.Marked, problem)
	}
	if !s.Stale(child) || s.Stale(sid) || s.Stale(peer) {
		t.Fatalf("stale: child=%v lead=%v peer=%v", s.Stale(child), s.Stale(sid), s.Stale(peer))
	}
	if _, problem := s.SessionRefresh(ctx, boxCaller(sid), single); problem != nil {
		t.Fatalf("ancestor: %v", problem)
	}
}

const restartedLine = "Your box restarted on the current version."

func refreshSelf(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) {
	t.Helper()
	response, problem := s.SessionRefresh(ctx, boxCaller(sid), contract.SessionRefreshRequest{Session: sid})
	if problem != nil {
		t.Fatal(problem)
	}
	if !slices.Equal(response.Marked, []contract.SessionID{sid}) {
		t.Fatalf("marked=%v, want %s", response.Marked, sid)
	}
}

func awaitTurn(t *testing.T, turns <-chan contract.TurnStart) contract.TurnStart {
	t.Helper()
	select {
	case start := <-turns:
		return start
	case <-time.After(5 * time.Second):
		t.Fatal("no turn started")
		return contract.TurnStart{}
	}
}

// wakingNotices counts every notice that could start a turn, read or not.
func wakingNotices(t *testing.T, s *Service, ctx context.Context) int {
	t.Helper()
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='session.notice' AND d.wake=1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSelfRefreshStopsAfterItsTurnAndComesBack(t *testing.T) {
	for _, target := range []string{"worker", "root"} {
		s, host, room, sid, ctx := staleSession(t)
		if target == "root" {
			if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", sid).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
				t.Fatal(problem)
			}
		}
		turn := openTurn(t, s, ctx, room, sid, "turn_open")
		turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
		turnInputs(t, turns)
		refreshSelf(t, s, ctx, sid)
		time.Sleep(loopSettles)
		passLifecycle(t, s, ctx)
		if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" {
			t.Fatalf("%s: stopped mid-turn: stops=%d want=%s", target, host.stops, boxWant(t, s, ctx, sid))
		}
		// Written now, it would be steered into this turn on the old binary, and nothing would be left to start the box.
		if got := noticeDeliveries(t, s, ctx); got != 0 {
			t.Fatalf("%s: deliveries=%d during the turn", target, got)
		}
		endTurn(t, s, ctx, sid, turn)
		waitWant(t, s, ctx, sid, "stop")
		passLifecycle(t, s, ctx)
		disconnect()
		if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped || boxWant(t, s, ctx, sid) != "run" {
			t.Fatalf("%s: stops=%d state=%s want=%s", target, host.stops, stateOf(t, s, ctx, sid), boxWant(t, s, ctx, sid))
		}
		before := turnCount(t, s, ctx, sid)
		passLifecycle(t, s, ctx)
		if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
			t.Fatalf("%s: starts=%d state=%s", target, host.starts, stateOf(t, s, ctx, sid))
		}
		if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
		turns, _ = connectBox(t, s, ctx, sid, newBinary)
		if text := turnInputs(t, turns); !strings.Contains(text, restartedLine) {
			t.Fatalf("%s: turn inputs=%q", target, text)
		}
		if s.Stale(sid) || turnCount(t, s, ctx, sid) != before+1 {
			t.Fatalf("%s: stale=%v turns=%d, want %d", target, s.Stale(sid), turnCount(t, s, ctx, sid), before+1)
		}
	}
}

// The job holds the box with nothing waiting, so nothing can start a turn on the old binary or stop and start the box in a loop.
func TestSelfRefreshHeldByJobDoesNotLoop(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	leaveJob(t, s, ctx, room, sid)
	turns := turnCount(t, s, ctx, sid)
	started, disconnect := connectBox(t, s, ctx, sid, newBinary)
	refreshSelf(t, s, ctx, sid)
	waitNotices(t, s, ctx, 1)
	if got := noticeText(t, s, ctx); got != heldByJob {
		t.Fatalf("notice=%q", got)
	}
	for range 3 {
		refreshSelf(t, s, ctx, sid)
		time.Sleep(loopSettles)
		passLifecycle(t, s, ctx)
	}
	select {
	case start := <-started:
		t.Fatalf("a turn started on the old binary: %+v", start)
	default:
	}
	if host.stops != 0 || host.starts != 0 || boxWant(t, s, ctx, sid) != "run" || turnCount(t, s, ctx, sid) != turns {
		t.Fatalf("stops=%d starts=%d want=%s turns=%d", host.stops, host.starts, boxWant(t, s, ctx, sid), turnCount(t, s, ctx, sid))
	}
	if got := updateNotices(t, s, ctx); got != 1 { // this also fails on any notice that wakes
		t.Fatalf("notices=%d", got)
	}

	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob, Tail: "built"}); problem != nil {
		t.Fatal(problem)
	}
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
	}
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, _ = connectBox(t, s, ctx, sid, newBinary)
	if text := turnInputs(t, started); !strings.Contains(text, "built") || !strings.Contains(text, restartedLine) || strings.Contains(text, "An update is ready") {
		t.Fatalf("turn inputs=%q", text)
	}
	if host.stops != 1 || host.starts != 1 {
		t.Fatalf("stops=%d starts=%d", host.stops, host.starts)
	}
}

// Nothing wakes the box loop when a terminal closes, so only the reconciler sees it, and the wake has to come from that stop too.
func TestSelfRefreshHeldByTerminalComesBackWhenItCloses(t *testing.T) {
	s, host, _, sid, ctx := staleSession(t)
	handle := openTerminal(t, s, ctx, sid)
	_, disconnect := connectBox(t, s, ctx, sid, newBinary)
	refreshSelf(t, s, ctx, sid)
	waitNotices(t, s, ctx, 1)
	time.Sleep(loopSettles)
	passLifecycle(t, s, ctx)
	if host.stops != 0 || boxWant(t, s, ctx, sid) != "run" || wakingNotices(t, s, ctx) != 0 {
		t.Fatalf("held by a terminal: stops=%d want=%s waking=%d", host.stops, boxWant(t, s, ctx, sid), wakingNotices(t, s, ctx))
	}
	handle.CloseTerminal()
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || wakingNotices(t, s, ctx) != 1 {
		t.Fatalf("stops=%d waking=%d", host.stops, wakingNotices(t, s, ctx))
	}
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
}

func TestRepeatedSelfRefreshSendsOneWake(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
	turnInputs(t, turns)
	for range 3 {
		refreshSelf(t, s, ctx, sid)
	}
	refreshBox(t, s, ctx, sid) // an outside mark in the same turn changes nothing
	endTurn(t, s, ctx, sid, turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if got := wakingNotices(t, s, ctx); got != 1 {
		t.Fatalf("waking notices=%d, want 1", got)
	}
	// The next restart is its own, with its own wake.
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	turns, _ = connectBox(t, s, ctx, sid, newBinary)
	start := awaitTurn(t, turns)
	refreshSelf(t, s, ctx, sid)
	endTurn(t, s, ctx, sid, start.Turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	if got := wakingNotices(t, s, ctx); got != 2 {
		t.Fatalf("waking notices=%d, want 2", got)
	}
}

// A box's self-refresh ends when it comes back, so an outside mark after that stays down until its next wake, as any does.
func TestOutsideRefreshAfterSelfRefreshLeavesNoWake(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
	turnInputs(t, turns)
	refreshSelf(t, s, ctx, sid)
	endTurn(t, s, ctx, sid, turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	turns, disconnect = connectBox(t, s, ctx, sid, newBinary)
	start := awaitTurn(t, turns)
	refreshBox(t, s, ctx, sid)
	endTurn(t, s, ctx, sid, start.Turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	passLifecycle(t, s, ctx)
	if host.stops != 2 || host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped || wakingNotices(t, s, ctx) != 1 {
		t.Fatalf("stops=%d starts=%d state=%s waking=%d", host.stops, host.starts, stateOf(t, s, ctx, sid), wakingNotices(t, s, ctx))
	}
}

// stoppedSelfRefresh leaves a session whose box has stopped with its restart notice still waiting.
func stoppedSelfRefresh(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, _, room, sid, ctx := staleSession(t)
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
	turnInputs(t, turns)
	refreshSelf(t, s, ctx, sid)
	endTurn(t, s, ctx, sid, turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	if got := noticeDeliveries(t, s, ctx); got != 1 {
		t.Fatalf("deliveries=%d, want the restart notice", got)
	}
	return s, room, sid, ctx
}

// A box can stop more than once before it says hello, if a start fails, and each stop reports.
func TestSelfRefreshSendsOneWakePerMarkAcrossStops(t *testing.T) {
	s, _, sid, ctx := stoppedSelfRefresh(t)
	s.BoxStopped(ctx, sid)
	if got := wakingNotices(t, s, ctx); got != 1 {
		t.Fatalf("waking notices=%d, want 1", got)
	}
}

func TestSelfRefreshNoticeIsNotHandedToParent(t *testing.T) {
	s, room, sid, ctx := stoppedSelfRefresh(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closing' WHERE id=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
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
