package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRecordPROpenIsAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir(), func() time.Time { return now })
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
	if err := s.RecordPROpen(ctx, "owner/repo", 2, "s", failing, []contract.Delivery{delivery, delivery}); err == nil {
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

	if err := s.RecordPROpen(ctx, "owner/repo", 1, "s", event, []contract.Delivery{delivery}); err != nil {
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
