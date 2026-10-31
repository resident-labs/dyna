package engine

import (
	"strings"
	"testing"

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
	closed, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: child})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, child, closed); err != nil {
		t.Fatal(err)
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
	frame, err := s.inputFrame(ctx, parent, contract.Event{From: owner, Body: contract.PostCreated{Text: "hello"}}, contract.Delivery{Surface: surface, Reply: contract.ReplyChannel{Kind: kind}, HandoffFrom: origin})
	if err != nil || !strings.Contains(frame, "worker") || !strings.Contains(frame, "closed") {
		t.Fatalf("frame %q: %v", frame, err)
	}
}

func TestPostRejectsClosingSession(t *testing.T) {
	s, room, child, ctx := setup(t)
	if err := s.store.MarkClosing(ctx, child, false); err != nil {
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
