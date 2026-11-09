package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestStaleReconnectSteersOwnerMessageIntoRunningTurn(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	firstCtx, cancel := context.WithCancel(ctx)
	ch, problem := testChannel(t, s, firstCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: start.Inputs[0].Delivery}); problem != nil {
		t.Fatal(problem)
	}
	cancel()
	for range ch {
	}
	s = New(s.store, fakeBoxes{}, fakeProfiles{}, setupNow)
	s.binary = "new-binary"
	reconnectCtx, stop := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(reconnectCtx, caller, "old-binary")
	if problem != nil {
		t.Fatal(problem)
	}
	defer func() {
		stop()
		for range messages {
		}
	}()
	select {
	case msg := <-messages:
		if _, ok := msg.Body.(contract.TurnStart); !ok {
			t.Fatalf("replay: %T", msg.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect did not replay")
	}
	if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "owner steer", Home: contract.Conversation{Kind: contract.ConversationDM, Session: session}}); problem != nil {
		t.Fatal(problem)
	}
	select {
	case msg := <-messages:
		if _, ok := msg.Body.(contract.TurnSteer); !ok {
			t.Fatalf("steer: %T", msg.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner message not steered")
	}
}
