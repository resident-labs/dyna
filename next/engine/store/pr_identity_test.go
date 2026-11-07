package store

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestTrackPRCaseVariantKeepsOneOwner(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES ('owner','room','owner','p','ready','',0),('sibling','room','sibling','p','ready','',0)`)
	if err != nil {
		t.Fatal(err)
	}
	// An open recorded PR, under the spelling used by the opener's room.
	err = s.TrackPR(ctx, TrackedPR{PrSummary: contract.PrSummary{Repo: "acme/app", Number: 7, Session: "owner", State: contract.PrOpen}})
	if err != nil {
		t.Fatal(err)
	}
	// TrackPR receives GitHub's canonical spelling from Pull.Base.Repo.FullName.
	err = s.TrackPR(ctx, TrackedPR{PrSummary: contract.PrSummary{Repo: "Acme/App", Number: 7, Session: "sibling", State: contract.PrOpen}})

	if !errors.Is(err, ErrPRTracked) {
		t.Fatalf("case variant claim = %v, want ErrPRTracked", err)
	}
	for _, session := range []contract.SessionID{"owner", "sibling"} {
		owns, err := s.OwnsPR(ctx, "acme/app", session, 7)
		if err != nil || owns != (session == "owner") {
			t.Fatalf("%s owns=%v err=%v", session, owns, err)
		}
	}
	err = s.RecordPROpen(ctx, "ACME/App", 7, "sibling", "branch", "title", "url", contract.Event{ID: "claim", Time: time.Now(), Room: "room", From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "claim"}}, nil)
	if !errors.Is(err, ErrPRTracked) {
		t.Fatalf("open case variant = %v", err)
	}
	// Inject a preexisting collision: neither row may authorize lifecycle writes.
	_, err = s.writer.ExecContext(ctx, `INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('Acme/App',7,'sibling','','')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []contract.SessionID{"owner", "sibling"} {
		owns, err := s.OwnsPR(ctx, "acme/app", session, 7)
		if err != nil || owns {
			t.Fatalf("ambiguous owner %s: %v %v", session, owns, err)
		}
		err = s.TrackPR(ctx, TrackedPR{PrSummary: contract.PrSummary{Repo: "ACME/APP", Number: 7, Session: session, State: contract.PrOpen}})
		if !errors.Is(err, ErrPRTracked) {
			t.Fatalf("ambiguous claim %s: %v", session, err)
		}
	}
}

func TestExistingCaseCollisionFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES ('owner','room','owner','p','ready','',0),('sibling','room','sibling','p','ready','',0);
 INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('acme/app',7,'owner','',''),('Acme/App',7,'sibling','','')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, sameOwner := range []bool{false, true} {
		if sameOwner {
			if _, err := s.writer.ExecContext(ctx, "UPDATE pr_owners SET session='owner'"); err != nil {
				t.Fatal(err)
			}
		}
		for _, session := range []contract.SessionID{"owner", "sibling"} {
			got, err := s.OwnsPR(ctx, "acme/app", session, 7)
			if err != nil || got {
				t.Errorf("collision (same owner %v) %s: %v %v", sameOwner, session, got, err)
			}
		}
	}
	if err := s.RecordPROpen(ctx, "ACME/APP", 7, "owner", "branch", "t", "u", contract.Event{ID: "claim", Time: time.Now(), Room: "room", From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "claim"}}, nil); !errors.Is(err, ErrPRTracked) {
		t.Errorf("ambiguous open claim: %v", err)
	}
	if _, err := s.PR(ctx, "acme/app", 7); err == nil {
		t.Error("ambiguous PR read succeeded")
	}
}

func TestTrackPRCaseVariantRetakesFinishedIdentity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES ('owner','room','owner','p','ready','',0),('sibling','room','sibling','p','ready','',0)`)
	if err != nil {
		t.Fatal(err)
	}
	row := TrackedPR{PrSummary: contract.PrSummary{Repo: "Acme/App", Number: 7, Session: "owner", State: contract.PrClosed}}
	if err := s.TrackPR(ctx, row); err != nil {
		t.Fatal(err)
	}
	row.Repo, row.Session, row.State = "acme/app", "sibling", contract.PrOpen
	if err := s.TrackPR(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackPR(ctx, row); err != nil {
		t.Fatal("same owner alias:", err)
	}
	stored, err := s.PR(ctx, "ACME/APP", 7)
	if err != nil || stored.Repo != "Acme/App" || stored.Session != "sibling" {
		t.Fatalf("retake: %+v %v", stored, err)
	}
	for _, session := range []contract.SessionID{"owner", "sibling", "Sibling"} {
		owns, err := s.OwnsPR(ctx, "ACME/APP", session, 7)
		if err != nil || owns != (session == "sibling") {
			t.Fatalf("%s owns=%v err=%v", session, owns, err)
		}
	}
}
