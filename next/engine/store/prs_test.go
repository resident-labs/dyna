package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRecordPROpenIsAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','ready','',0)"); err != nil {
		t.Fatal(err)
	}
	from := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	event := contract.Event{ID: "opened", Time: now, Room: "room", From: from, Body: contract.PostCreated{Text: "PR opened"}}
	delivery := contract.Delivery{To: from, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}

	// The second delivery repeats the first, so the transaction fails after the owner and event rows are written.
	failing := contract.Event{ID: "failing", Time: now, Room: "room", From: from, Body: contract.PostCreated{Text: "PR opened"}}
	if err := s.RecordPROpen(ctx, "owner/repo", 2, "s", "v2/test", "title", "url", failing, []contract.Delivery{delivery, delivery}); err == nil {
		t.Fatal("duplicate delivery accepted")
	}
	var owners, events, deliveries int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM pr_owners WHERE number=2").Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE id='failing'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if owners != 0 || events != 0 {
		t.Fatalf("failed open kept owner %d and event %d", owners, events)
	}

	if err := s.RecordPROpen(ctx, "owner/repo", 1, "s", "v2/test", "title", "url", event, []contract.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	var session, goal, approved string
	if err := s.Read().QueryRowContext(ctx, "SELECT session,goal,approved_head FROM pr_owners WHERE repo='owner/repo' AND number=1").Scan(&session, &goal, &approved); err != nil {
		t.Fatal(err)
	}
	if session != "s" || goal != "" || approved != "" {
		t.Fatalf("owner %q goal %q approved %q", session, goal, approved)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND seq=(SELECT seq FROM events WHERE id='opened')").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestCloseHandsPRsThroughSubtree(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "room", "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES ('root','room','root',NULL,'p','ready','',0),('parent','room','parent','root','p','ready','',0),('leaf','room','leaf','parent','p','ready','',0)")
	if err != nil {
		t.Fatal(err)
	}
	event := func(id string) contract.Event {
		return contract.Event{ID: contract.EventID(id), Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: id}}
	}
	if err := s.RecordPROpen(ctx, "owner/repo", 1, "leaf", "v2/test", "title", "url", event("open-leaf"), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPROpen(ctx, "owner/repo", 2, "parent", "v2/test", "title", "url", event("open-parent"), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSession(ctx, "leaf", event("close-leaf")); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSession(ctx, "parent", event("close-parent")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM pr_owners WHERE session='root'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("handed off %d PRs: %v", count, err)
	}
	if err := s.RecordPROpen(ctx, "owner/repo", 3, "leaf", "v2/test", "title", "url", event("late-open"), nil); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := s.Read().QueryRowContext(ctx, "SELECT session FROM pr_owners WHERE number=3").Scan(&owner); err != nil || owner != "root" {
		t.Fatalf("late PR owner = %q: %v", owner, err)
	}
	if err := s.CloseSession(ctx, "root", event("close-root")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPROpen(ctx, "owner/repo", 4, "leaf", "v2/test", "title", "url", event("no-owner"), nil); err == nil {
		t.Fatal("accepted PR with no open ancestor")
	}
}

func TestTrackPRKeepsAnOpenOwnerAndRetakesAFinishedRow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES ('a','room','a',NULL,'p','ready','',0),('b','room','b','a','p','ready','',0)"); err != nil {
		t.Fatal(err)
	}
	row := func(session contract.SessionID) TrackedPR {
		return TrackedPR{HeadRepo: "fork/repo", PrSummary: contract.PrSummary{Repo: "upstream/repo", Number: 1, URL: "u", Title: "t", Author: "x", Session: session, State: contract.PrOpen, Head: "h"}}
	}
	if err := s.TrackPR(ctx, row("a")); err != nil {
		t.Fatal("a repository no room lists must be trackable:", err)
	}
	if err := s.TrackPR(ctx, row("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackPR(ctx, row("b")); !errors.Is(err, ErrPRTracked) {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE pr_owners SET state='merged',branch='old',ci='success',baseline=0"); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackPR(ctx, row("b")); err != nil {
		t.Fatal(err)
	}
	got, err := s.PR(ctx, "upstream/repo", 1)
	if err != nil || got.Session != "b" || got.State != contract.PrOpen || got.CI != "" || !got.Baseline {
		t.Fatalf("%+v %v", got, err)
	}
	if owns, err := s.OwnsPRBranch(ctx, "upstream/repo", "b", "old"); err != nil || owns {
		t.Fatalf("a new owner inherited the branch: %v %v", owns, err)
	}
}
