package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeArchiveVerifier struct {
	err   error
	calls int
}

func (v *fakeArchiveVerifier) Verify(_ context.Context, _ contract.SessionID, _ contract.ArchiveDoneRequest) error {
	v.calls++
	return v.err
}

func TestCloseArchivesBeforeRemovingBox(t *testing.T) {
	s, room, session, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeArchiveVerifier{}
	s.SetArchiveVerifier(verifier)
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel // the initial turn
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
		done <- e
	}()
	msg := <-channel
	if _, ok := msg.Body.(contract.Archive); !ok {
		t.Fatalf("expected archive, got %+v", msg.Body)
	}
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	var commit string
	err := s.store.Read().QueryRowContext(ctx, "SELECT commit_sha FROM archives WHERE session=? AND repo=?", session, "owner/repo").Scan(&commit)
	if err != nil || commit != "abc" || verifier.calls != 1 {
		t.Fatalf("commit %q, verifier %+v, error %v", commit, verifier, err)
	}
}

func TestReconnectResendsArchiveAndWaitsForAllRepos(t *testing.T) {
	s, room, session, ctx := setup(t)
	for _, name := range []contract.RepoName{"owner/one", "owner/two"} {
		if err := s.store.AddRoomRepo(ctx, room, name, false); err != nil {
			t.Fatal(err)
		}
	}
	s.SetArchiveVerifier(&fakeArchiveVerifier{})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first, e := s.BoxChannel(ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	<-first
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
		done <- e
	}()
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
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives").Scan(&count); err != nil || count != 2 {
		t.Fatalf("archive rows: %d, %v", count, err)
	}
}

func TestCloseKeepsBoxOnArchiveMismatch(t *testing.T) {
	s, room, session, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	s.SetArchiveVerifier(&fakeArchiveVerifier{err: errors.New("archive ref mismatch")})
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
		done <- e
	}()
	<-channel
	_, e = s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
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

func TestDiscardSkipsArchive(t *testing.T) {
	s, room, session, ctx := setup(t)
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
