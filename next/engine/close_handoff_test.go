package engine

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCloseHandsPendingInputToParent(t *testing.T) {
	s, room, child, ctx := setup(t)
	var parent contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", child).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	post, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: child}, Text: "hello"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	childStart, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.store.Dispatch(ctx, child, "child-turn", childStart, false); err != nil {
		t.Fatal(err)
	}
	collision, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "both", Mentions: []string{"@root", "@worker"}})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id=? AND seq=?", parent, collision.Seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closed, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: child})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, child, closed); err != nil {
		t.Fatal(err)
	}
	var state string
	var childCopies int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state,(SELECT COUNT(*) FROM deliveries WHERE to_id=? AND seq=?) FROM deliveries WHERE to_id=? AND seq=?", child, collision.Seq, parent, collision.Seq).Scan(&state, &childCopies); err != nil || state != "read" || childCopies != 0 {
		t.Fatalf("collision %s %d: %v", state, childCopies, err)
	}
	var origin contract.SessionID
	var surface contract.Surface
	var kind contract.ReplyKind
	var wake bool
	if err := s.store.Read().QueryRowContext(ctx, "SELECT handoff_from,surface,reply_kind,wake FROM deliveries WHERE to_id=? AND seq=? AND state='pending'", parent, post.Seq).Scan(&origin, &surface, &kind, &wake); err != nil {
		t.Fatal(err)
	}
	if origin != child || surface != contract.SurfaceSession || kind != contract.ReplyPost || !wake {
		t.Fatalf("handoff %s %s %s %v", origin, surface, kind, wake)
	}
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	started, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	_, inputs, _, err := s.store.Dispatch(ctx, parent, "handoff-turn", started, false)
	if err != nil {
		t.Fatal(err)
	}
	var inputFound bool
	for _, input := range inputs {
		if int64(input.Seq) == post.Seq {
			inputFound = input.HandoffFrom == child
		}
	}
	if !inputFound {
		t.Fatalf("handoff not dispatched: %+v", inputs)
	}
	frame, _, err := s.inputFrame(ctx, parent, contract.Event{From: owner, Body: contract.PostCreated{Text: "hello"}}, contract.Delivery{Surface: surface, Reply: contract.ReplyChannel{Kind: kind}, HandoffFrom: origin})
	if err != nil || !strings.Contains(frame, "worker") || !strings.Contains(frame, "closed") {
		t.Fatalf("frame %q: %v", frame, err)
	}
}

func TestPostRejectsClosingSession(t *testing.T) {
	s, room, child, ctx := setup(t)
	if err := s.store.MarkClosing(ctx, child, false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, req := range []contract.PostRequest{
		{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: child}, Text: "hello"},
		{Room: room, Text: "hello", Mentions: []string{"@worker"}},
	} {
		if _, problem := s.Post(ctx, owner, req); problem == nil || problem.Code != contract.ErrSessionClosing {
			t.Fatalf("post: %v", problem)
		}
	}
}
