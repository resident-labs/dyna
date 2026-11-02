package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func setupArchive(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, root, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: root, Name: "child", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	return s, room, child.Session, ctx
}

func TestSubtreeArchivesChildBeforeParent(t *testing.T) {
	s, room, parent, ctx := setupArchive(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "grandchild", Profile: "test", Parent: parent, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	s.SetRepositories(&fakeRepos{})
	parentCaller := contract.Principal{Kind: contract.PrincipalSession, Session: parent}
	childCaller := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	parentChannel, e := testChannel(t, s, ctx, parentCaller)
	if e != nil {
		t.Fatal(e)
	}
	<-parentChannel
	childChannel, e := testChannel(t, s, ctx, childCaller)
	if e != nil {
		t.Fatal(e)
	}
	<-childChannel
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: parent}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, ok := (<-childChannel).Body.(contract.Archive); !ok {
		t.Fatal("child archive not sent")
	}
	select {
	case msg := <-parentChannel:
		t.Fatalf("parent started early: %+v", msg)
	default:
	}
	if _, e := s.ArchiveDone(ctx, childCaller, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx) // Remove the child's box.
	passLifecycle(t, s, ctx) // Close the child session.
	passLifecycle(t, s, ctx) // Start the parent's archive.
	if state := stateOf(t, s, ctx, parent); state != contract.BoxClosing {
		t.Fatalf("parent box state = %s, want closing", state)
	}
	if _, e := s.ArchiveDone(ctx, parentCaller, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"session.closed"})
	if err != nil || len(events) != 2 || events[0].Body.(contract.SessionClosed).Session != child.Session || events[1].Body.(contract.SessionClosed).Session != parent {
		t.Fatalf("close order: %+v, %v", events, err)
	}
}

func TestCloseStartsStoppedBoxForArchive(t *testing.T) {
	s, _, sid, ctx := setupArchive(t)
	h := &dormantHost{}
	s.boxes = h
	resetLifecycle(s)
	s.SetRepositories(&fakeRepos{})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	channelCtx, cancel := context.WithCancel(ctx)
	channel, problem := testChannel(t, s, channelCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-channel).Body.(contract.TurnStart)
	cancel()
	for range channel {
	}
	if _, problem = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000, 0).Add(11 * time.Minute) }
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("box did not stop")
	}
	if _, problem = s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if h.starts != 1 {
		t.Fatal("closing did not restart the box")
	}
	if _, problem = s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	reconnected, problem := s.BoxChannel(ctx, caller, "")
	if problem != nil {
		t.Fatal(problem)
	}
	if _, ok := (<-reconnected).Body.(contract.Archive); !ok {
		t.Fatal("archive not sent")
	}
}

func TestCloseArchivesBeforeRemovingBox(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeRepos{}
	s.SetRepositories(verifier)
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel // the initial turn
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	msg := <-channel
	if _, ok := msg.Body.(contract.Archive); !ok {
		t.Fatalf("expected archive, got %+v", msg.Body)
	}
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	var commit string
	err := s.store.Read().QueryRowContext(ctx, "SELECT commit_sha FROM archives WHERE session=? AND repo=?", session, "owner/repo").Scan(&commit)
	if err != nil || commit != "abc" || verifier.calls != 1 {
		t.Fatalf("commit %q, verifier %+v, error %v", commit, verifier, err)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"session.closed"})
	if err != nil || len(events) != 1 || events[0].From.Kind != contract.PrincipalEngine {
		t.Fatalf("close events %+v, error %v", events, err)
	}
}

// failArchive fails five archives in a row, which ends the retries, and lets the reconciler stop the box.
func failArchive(t *testing.T, s *Service, ctx context.Context, caller contract.Principal, channel <-chan contract.BoxMessage) {
	t.Helper()
	for try := 1; try <= 5; try++ {
		if _, ok := (<-channel).Body.(contract.Archive); !ok {
			t.Fatalf("try %d: expected archive", try)
		}
		if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Error: "push refused"}); e != nil {
			t.Fatal(e)
		}
		if try < 5 {
			later := s.now().Add(10 * time.Minute)
			s.now = func() time.Time { return later }
			passLifecycle(t, s, ctx)
		}
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
}

// heldBox reaches a box whose close stopped retrying. disconnect drops its channel, as the stopped container would.
func heldBox(t *testing.T) (s *Service, host *dormantHost, room contract.RoomID, session contract.SessionID, ctx context.Context, disconnect func()) {
	t.Helper()
	s, room, session, ctx = setupArchive(t)
	host = &dormantHost{}
	s.boxes = host
	resetLifecycle(s)
	s.SetRepositories(&fakeRepos{})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	connected, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	channel, e := testChannel(t, s, connected, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	failArchive(t, s, ctx, caller, channel)
	return s, host, room, session, ctx, func() {
		t.Helper()
		cancel()
		for range channel {
		}
		for deadline := time.Now().Add(5 * time.Second); s.Connected(session); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("box stayed connected")
			}
		}
	}
}

func TestArchiveThatKeepsFailingStopsTheBoxAndKeepsItsVolume(t *testing.T) {
	s, host, room, session, ctx, disconnect := heldBox(t)
	record, err := s.store.Session(ctx, session) // Still open, and so still closing.
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxStopped || current.Reason != "push refused" {
		t.Fatalf("box %+v, %v", current, err)
	}
	if host.stops != 1 || host.starts != 0 || slices.Contains(host.removed, record.Box) {
		t.Fatalf("stops %d starts %d removed %v", host.stops, host.starts, host.removed)
	}
	var state, want string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT s.state,b.want FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=?", session).Scan(&state, &want); err != nil || state != "closing" || want != "hold" {
		t.Fatalf("state %s want %s: %v", state, want, err)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"session.notice"})
	if err != nil || len(events) != 1 {
		t.Fatalf("notices: %+v, %v", events, err)
	}
	text := events[0].Body.(contract.SessionNotice).Text
	for _, part := range []string{"Its volume is kept", "dyna session close " + string(session), "--discard", "Last failure: push refused"} {
		if !strings.Contains(text, part) {
			t.Fatalf("notice %q lacks %q", text, part)
		}
	}
	var wakes int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE seq=? AND wake=1", events[0].Seq).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatalf("wakes: %d, %v", wakes, err)
	}

	disconnect()
	for range 3 {
		later := s.now().Add(time.Hour)
		s.now = func() time.Time { return later }
		passLifecycle(t, s, ctx)
	}
	if host.starts != 0 || host.stops != 1 || slices.Contains(host.removed, record.Box) {
		t.Fatalf("a held box was touched: stops %d starts %d removed %v", host.stops, host.starts, host.removed)
	}

	// The close call starts the archive again, with five more tries.
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, session) != contract.BoxClosing || host.starts != 1 {
		t.Fatalf("retry: state %s starts %d", stateOf(t, s, ctx, session), host.starts)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	for message := range channel {
		if _, ok := message.Body.(contract.Archive); ok {
			break
		}
	}
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, err := s.store.Session(ctx, session); err == nil {
		t.Fatal("session did not close after its archive")
	}
}

func TestHeldBoxDiscardsOnlyWhenTheOwnerSays(t *testing.T) {
	s, host, _, session, ctx, disconnect := heldBox(t)
	disconnect()
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session, Discard: true}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if !slices.Contains(host.removed, record.Box) {
		t.Fatalf("removed %v", host.removed)
	}
	if _, err := s.store.Session(ctx, session); err == nil {
		t.Fatal("session did not close")
	}
}

func TestCloseKeepsBoxOnArchiveMismatch(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{err: errors.New("archive ref mismatch")})
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	<-channel
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.store.Box(ctx, record.Box)
	if err != nil || box.State != contract.BoxClosing || box.Reason != "archive ref mismatch" {
		t.Fatalf("box %+v, error %v", box, err)
	}
}

type archiveRestartBoxes struct {
	fakeBoxes
	started chan contract.BoxID
}

func (b archiveRestartBoxes) Start(_ context.Context, id contract.BoxID) error {
	b.started <- id
	return nil
}

func TestRestartResendsArchiveAndFinishesClose(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	connectAgent(ctx, t, s, session)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := testChannel(t, s, ctx, caller); e != nil {
		t.Fatal(e)
	}
	_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	boxes := archiveRestartBoxes{started: make(chan contract.BoxID, 1)}
	restarted := New(s.store, boxes, fakeProfiles{}, s.now)
	restarted.SetRepositories(&fakeRepos{})
	passLifecycle(t, restarted, ctx)
	select {
	case id := <-boxes.started:
		if id != record.Box {
			t.Fatalf("started box %s, want %s", id, record.Box)
		}
	default:
		t.Fatal("restart did not start the box for archive")
	}
	channel, e := testChannel(t, restarted, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive after restart")
	}
	_, e = restarted.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, restarted, ctx)
	passLifecycle(t, restarted, ctx)
	current, err := s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v", current, err)
	}
}
