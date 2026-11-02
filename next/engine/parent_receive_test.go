package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// receiveTree makes root > worker > kid, where kid is the session whose parent may not receive.
func receiveTree(t *testing.T) (*Service, contract.RoomID, contract.SessionID, contract.SessionID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, worker, ctx := setup(t)
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	kid, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.SessionStartRequest{Room: room, Name: "kid", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	return s, room, root, worker, kid.Session, ctx
}

func setBox(t *testing.T, s *Service, ctx context.Context, session contract.SessionID, state contract.BoxState, reason string) {
	t.Helper()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state=?,reason=? WHERE session=?", state, reason, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func pendingTo(t *testing.T, s *Service, ctx context.Context, session contract.SessionID) int {
	t.Helper()
	var n int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE to_kind='session' AND to_id=? AND state='pending'", session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChildPostToDeadParentIsRefused(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	from := contract.Principal{Kind: contract.PrincipalSession, Session: kid}
	before := pendingTo(t, s, ctx, worker)
	for _, c := range []struct {
		state  contract.BoxState
		reason string
		want   string
	}{
		{contract.BoxFailed, "host removed", "your parent worker can't receive: its box failed (host removed). Deliver to root (mention root) until it's back."},
		{contract.BoxLost, "", "your parent worker can't receive: its box was lost. Deliver to root (mention root) until it's back."},
		{contract.BoxPending, "no-ready-host", "your parent worker can't receive: its box can't be placed (no-ready-host). Deliver to root (mention root) until it's back."},
	} {
		setBox(t, s, ctx, worker, c.state, c.reason)
		_, problem := s.Post(ctx, from, contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@worker"}})
		if problem == nil || problem.Code != contract.ErrConflict || problem.Message != c.want {
			t.Fatalf("%s: %+v", c.state, problem)
		}
	}
	if n := pendingTo(t, s, ctx, worker); n != before {
		t.Fatalf("refused posts queued %d inputs", n-before)
	}
	// A post that names root instead goes through.
	if _, problem := s.Post(ctx, from, contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@root"}}); problem != nil {
		t.Fatal(problem)
	}
}

func TestChildPostToClosingParentIsRefused(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closing' WHERE id=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: kid}, contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@worker"}})
	want := "your parent worker can't receive: it is closing. Deliver to root (mention root) until it's back."
	if problem == nil || problem.Message != want {
		t.Fatalf("post: %+v", problem)
	}
}

func TestChildPostResumesAfterParentRecovers(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	from := contract.Principal{Kind: contract.PrincipalSession, Session: kid}
	setBox(t, s, ctx, worker, contract.BoxFailed, "host removed")
	if _, problem := s.Post(ctx, from, contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@worker"}}); problem == nil {
		t.Fatal("post to a failed parent went through")
	}
	setBox(t, s, ctx, worker, contract.BoxStopped, "")
	before := pendingTo(t, s, ctx, worker)
	if _, problem := s.Post(ctx, from, contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@worker"}}); problem != nil {
		t.Fatal(problem)
	}
	if n := pendingTo(t, s, ctx, worker); n != before+1 {
		t.Fatalf("pending %d, want %d", n, before+1)
	}
}

// closeKid gives kid two unread posts and closes it, returning the notices each session got.
func closeKid(t *testing.T, s *Service, room contract.RoomID, kid contract.SessionID, ctx context.Context, before func()) map[contract.SessionID][]string {
	t.Helper()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for range 2 {
		if _, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: kid}, Text: "stale"}); problem != nil {
			t.Fatal(problem)
		}
	}
	before()
	closed, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: kid})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, kid, closed); err != nil {
		t.Fatal(err)
	}
	if n := pendingTo(t, s, ctx, kid); n != 0 {
		t.Fatalf("kid keeps %d inputs", n)
	}
	rows, err := s.store.Read().QueryContext(ctx, "SELECT d.to_id,json_extract(e.body,'$.text') FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='session.notice' AND d.state='pending'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	notices := make(map[contract.SessionID][]string)
	for rows.Next() {
		var to contract.SessionID
		var text string
		if err := rows.Scan(&to, &text); err != nil {
			t.Fatal(err)
		}
		notices[to] = append(notices[to], text)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return notices
}

func TestCloseDropsUndeliveredWithOneNotice(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	rootBefore := pendingTo(t, s, ctx, root)
	notices := closeKid(t, s, room, kid, ctx, func() {})
	want := "kid closed; 2 undelivered messages to it were dropped."
	if len(notices) != 1 || len(notices[worker]) != 1 || notices[worker][0] != want {
		t.Fatalf("notices %v", notices)
	}
	if n := pendingTo(t, s, ctx, root); n != rootBefore {
		t.Fatalf("root got %d inputs", n-rootBefore)
	}
}

func TestCloseNoticeSkipsClosingParent(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	notices := closeKid(t, s, room, kid, ctx, func() {
		if err := s.store.MarkClosing(ctx, worker, false, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	})
	want := "kid closed; 2 undelivered messages to it were dropped."
	if len(notices) != 1 || len(notices[root]) != 1 || notices[root][0] != want {
		t.Fatalf("notices %v", notices)
	}
}
