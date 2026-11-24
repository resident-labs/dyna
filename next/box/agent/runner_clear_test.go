package agent

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/fakeharness"
)

func TestRunnerCallsNewConversationBeforeFreshTurn(t *testing.T) {
	h := &fakeharness.Harness{
		Turns: []fakeharness.Turn{
			{Result: contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "first"}},
			{Result: contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "fresh"}},
		},
	}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 2)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()

	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{
		Turn:   "turn_1",
		Inputs: []contract.TurnInput{{Delivery: 1, Text: "before"}},
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn 1 did not finish")
	}
	if h.FreshStarts != 0 {
		t.Fatalf("expected 0 fresh starts, got %d", h.FreshStarts)
	}

	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{
		Turn:   "turn_2",
		Inputs: []contract.TurnInput{{Delivery: 2, Text: "after"}},
		Fresh:  true,
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn 2 did not finish")
	}
	if h.FreshStarts != 1 {
		t.Fatalf("expected 1 fresh start, got %d", h.FreshStarts)
	}
}

// Replaying a Fresh turn whose input was already delivered does not wipe the conversation again.
func TestRunnerDoesNotResetFreshTurnAlreadyDelivered(t *testing.T) {
	h := &fakeharness.Harness{
		Turns: []fakeharness.Turn{
			{Result: contract.TurnEnded{Outcome: contract.TurnCompleted, Final: "resumed"}},
		},
	}
	h.Consume(10)
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}

	if err := runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{
		Turn:   "turn_replay",
		Inputs: []contract.TurnInput{{Delivery: 10, Text: "already delivered"}},
		Fresh:  true,
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.done:
	case <-time.After(2 * time.Second):
		t.Fatal("replayed turn did not finish")
	}
	if h.FreshStarts != 0 {
		t.Fatalf("replayed fresh turn should not call NewConversation, got %d", h.FreshStarts)
	}
}
