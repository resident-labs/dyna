package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func setupArchive(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, root, ctx := setup(t)
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: root, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	return s, room, child.Session, ctx
}

func TestCloseArchivesBeforeRemovingBox(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeRepos{}
	s.SetRepositories(verifier)
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel // the initial turn
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	msg := <-channel
	if _, ok := msg.Body.(contract.Archive); !ok {
		t.Fatalf("expected archive, got %+v", msg.Body)
	}
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
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

func TestReconnectResendsArchiveAndWaitsForAllRepos(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	for _, name := range []contract.RepoName{"owner/one", "owner/two"} {
		if err := s.store.AddRoomRepo(ctx, room, name, false); err != nil {
			t.Fatal(err)
		}
	}
	s.SetRepositories(&fakeRepos{commits: map[contract.RepoName]string{"owner/one": "first", "owner/two": "second"}})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first, e := s.BoxChannel(ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-first
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-first).Body.(contract.Archive); !ok {
		t.Fatal("expected archive")
	}
	_, e = s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/one", Commit: "first"})
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial archive rows: %d, %v", count, err)
	}
	second, e := s.BoxChannel(ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-second).Body.(contract.Archive); !ok {
		t.Fatal("expected archive after reconnect")
	}
	_, e = s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/two", Commit: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives").Scan(&count); err != nil || count != 2 {
		t.Fatalf("archive rows: %d, %v", count, err)
	}
}

func TestCloseRetryResendsArchiveOnLiveChannel(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := s.BoxChannel(ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive")
	}
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{Repo: "owner/repo", Error: "failed"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-channel).Body.(contract.Archive); !ok {
		t.Fatal("expected archive on retry")
	}
}

func TestCloseKeepsBoxOnArchiveMismatch(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{err: errors.New("archive ref mismatch")})
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	<-channel
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.store.Box(ctx, record.Box)
	if err != nil || box.State != contract.BoxClosing || box.Reason != "archive ref mismatch" {
		t.Fatalf("box %+v, error %v", box, err)
	}
}

func TestRestartResendsArchiveAndFinishesClose(t *testing.T) {
	s, room, session, ctx := setupArchive(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
	if e != nil {
		t.Fatal(e)
	}
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(s.store, fakeBoxes{}, fakeProfiles{}, s.now)
	restarted.SetRepositories(&fakeRepos{})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := restarted.BoxChannel(ctx, caller)
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
	current, err = s.transition(ctx, record.Room, current, contract.BoxClosing, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.transition(ctx, record.Room, current, contract.BoxDeleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeClosures(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Session(ctx, session); err == nil {
		t.Fatal("session remained open")
	}
}

func TestRestoreFinishesCloseWithoutRepositories(t *testing.T) {
	s, _, session, ctx := setupArchive(t)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, record.Room, current, contract.BoxClosing, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeClosures(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = s.store.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v", current, err)
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
	box, err := s.store.Box(ctx, record.Box)
	if err != nil || box.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v", box, err)
	}
}
