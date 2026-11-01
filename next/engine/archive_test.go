package engine

import (
	"context"
	"database/sql"
	"errors"
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

func TestClosePendingBoxWithoutTurnsDoesNotStart(t *testing.T) {
	s, _, sid, ctx := setupArchive(t)
	h := &dormantHost{}
	s.boxes = h
	resetLifecycle(s)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='pending',image='image' WHERE id=?", session.Box)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxDeleted || h.starts != 0 {
		t.Fatalf("box=%+v starts=%d err=%v", b, h.starts, err)
	}
}

type heldArchiveStart struct {
	fakeBoxes
	started, release chan struct{}
}

func (h heldArchiveStart) Start(context.Context, contract.BoxID) error {
	close(h.started)
	<-h.release
	return nil
}

func TestCloseBlocksDispatchBeforeArchive(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	wakeSession(t, s, ctx, room, sid)
	h := heldArchiveStart{started: make(chan struct{}), release: make(chan struct{})}
	s.boxes = h
	resetLifecycle(s)
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	<-h.started
	channel, problem := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, "")
	if problem != nil {
		t.Fatal(problem)
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("closing box dispatched a turn")
	}
	close(h.release)
	s.reconciler.Wait()
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM turns WHERE session=?", sid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("turns=%d err=%v", count, err)
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

func TestReconnectArchivesOnlySessionRepository(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/other", false); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-first
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	<-first
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/other", Commit: "abc"}); e == nil {
		t.Fatal("other repository archive was accepted")
	}
	second, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-second).Body.(contract.Archive); !ok {
		t.Fatal("expected archive after reconnect")
	}
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives WHERE session=? AND repo=?", session, "owner/repo").Scan(&count); err != nil || count != 1 {
		t.Fatalf("archive rows: %d, %v", count, err)
	}
}

func TestCloseRetryResendsArchiveOnLiveChannel(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive")
	}
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Error: "failed"}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxClosing || current.Reason != "" {
		t.Fatalf("retry did not clear archive failure: box=%+v err=%v", current, err)
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive on retry")
	}
}

func TestArchiveFailureRetriesOnTimerAndKeepsBox(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive")
	}
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Error: "no space left on device"}); e != nil {
		t.Fatal(e)
	}
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	reason := func() string {
		t.Helper()
		current, err := s.store.Box(ctx, record.Box)
		if err != nil || current.State != contract.BoxClosing {
			t.Fatalf("box left closing: %+v, %v", current, err)
		}
		return current.Reason
	}
	start := s.now()
	s.now = func() time.Time { return start.Add(10*time.Minute - time.Second) }
	passLifecycle(t, s, ctx)
	if reason() == "" {
		t.Fatal("retried before the timer")
	}
	s.now = func() time.Time { return start.Add(10 * time.Minute) }
	passLifecycle(t, s, ctx)
	if reason() != "" {
		t.Fatal("timer did not clear the archive failure")
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive on the timed retry")
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

func TestRestoreFinishesSessionAfterBoxDeleted(t *testing.T) {
	s, _, session, ctx := setupArchive(t)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	current, err = s.transition(ctx, record.Room, current, contract.BoxClosing, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.transition(ctx, record.Room, current, contract.BoxDeleted, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkClosing(ctx, session, false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.store, fakeBoxes{}, fakeProfiles{}, s.now)
	passLifecycle(t, restarted, ctx)
	if _, err := s.store.Session(ctx, session); err == nil {
		t.Fatal("session remained open")
	}
}

func TestRestoreStartsMarkedLeaf(t *testing.T) {
	s, room, parent, ctx := setup(t)
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	connectAgent(ctx, t, s, child.Session)
	connectAgent(ctx, t, s, parent)
	record, err := s.store.Session(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, room, current, contract.BoxClosing, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkClosing(ctx, parent, false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.store, fakeBoxes{}, fakeProfiles{}, s.now)
	passLifecycle(t, restarted, ctx)
	childAck := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	if _, e := restarted.ArchiveDone(ctx, childAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, restarted, ctx)
	passLifecycle(t, restarted, ctx)
	parentAck := contract.Principal{Kind: contract.PrincipalSession, Session: parent}
	if _, e := restarted.ArchiveDone(ctx, parentAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, restarted, ctx)
	passLifecycle(t, restarted, ctx)
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"session.closed"})
	if err != nil || len(events) != 2 || events[0].Body.(contract.SessionClosed).Session != child.Session || events[1].Body.(contract.SessionClosed).Session != parent {
		t.Fatalf("close order: %+v, %v", events, err)
	}
}

func TestRestoreWaitsForArchiveAckWithoutRepositories(t *testing.T) {
	s, _, session, ctx := setup(t)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	connectAgent(ctx, t, s, session)
	current, err = s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, record.Room, current, contract.BoxClosing, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkClosing(ctx, session, false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.store, fakeBoxes{}, fakeProfiles{}, s.now)
	passLifecycle(t, restarted, ctx)
	current, err = s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxClosing {
		t.Fatalf("box %+v, error %v, want closing until the box acknowledges the archive", current, err)
	}
}

func TestCloseWaitsForArchiveAckWithoutRepositories(t *testing.T) {
	s, room, root, ctx := setup(t)
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: root, Name: "child", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel // the initial turn
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: child.Session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	msg := <-channel
	if _, ok := msg.Body.(contract.Archive); !ok {
		t.Fatalf("expected archive, got %+v", msg.Body)
	}
	record, err := s.store.Session(ctx, child.Session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxClosing {
		t.Fatalf("box %+v, error %v, want closing before the archive ack", current, err)
	}
	if _, e := s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	current, err = s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v, want deleted after the archive ack", current, err)
	}
}

func TestDiscardSkipsArchive(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session, Discard: true})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	box, err := s.store.Box(ctx, record.Box)
	if err != nil || box.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v", box, err)
	}
}
