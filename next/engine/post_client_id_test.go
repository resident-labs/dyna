package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestPostStoresToolCallID(t *testing.T) {
	s, room, worker, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	post, problem := s.Post(ctx, caller, contract.PostRequest{Room: room, Text: "hello", ToolCallID: "call-1"})
	if problem != nil {
		t.Fatal(problem)
	}
	event, err := s.store.Event(ctx, post.Seq)
	if err != nil || event.Body.(contract.PostCreated).ToolCallID != "call-1" {
		t.Fatalf("post event = %+v, err = %v", event, err)
	}
}

func TestPostClientIDRepeatReturnsTheFirstPost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	first, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "hello", ClientID: "c1"})
	if problem != nil {
		t.Fatal(problem)
	}
	again, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "hello", ClientID: "c1"})
	if problem != nil || again != first {
		t.Fatalf("repeat: %+v, want %+v: %v", again, first, problem)
	}
	other, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "hello", ClientID: "c1"})
	if problem != nil || other.Event == first.Event {
		t.Fatalf("another author with the same ID: %+v: %v", other, problem)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"post.created"})
	if err != nil || len(events) != 2 {
		t.Fatalf("%d post events, want 2: %v", len(events), err)
	}
	if echo := events[0].Body.(contract.PostCreated).ClientID; echo != "c1" {
		t.Fatalf("post.created clientId %q", echo)
	}
	// A retry answers from the first post even when its mention can no longer resolve.
	mention := contract.PostRequest{Room: room, Text: "@worker hi", Mentions: []string{"worker"}, ClientID: "c2"}
	firstMention, problem := s.Post(ctx, owner, mention)
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: worker, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if again, problem := s.Post(ctx, owner, mention); problem != nil || again != firstMention {
		t.Fatalf("retry after the mentioned session closed: %+v, want %+v: %v", again, firstMention, problem)
	}
	if _, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "long", ClientID: strings.Repeat("x", 65)}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("65 characters: %v", problem)
	}
}
