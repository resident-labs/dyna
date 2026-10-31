package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/fakeharness"
)

type harnessFake struct {
	mu         sync.Mutex
	started    chan struct{}
	release    chan struct{}
	permit     chan struct{}
	steers     int
	recoveries int
	delivered  map[contract.DeliverySeq]bool
}

func (h *harnessFake) Start(_ context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	for _, input := range inputs {
		h.delivered[input.Delivery] = true
	}
	h.mu.Unlock()
	close(h.started)
	if h.permit != nil {
		<-h.permit
	}
	started()
	<-h.release
	return contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed")}, nil
}
func (h *harnessFake) SetModel(context.Context, contract.ModelChoice) error { return nil }
func (h *harnessFake) Continue(context.Context, contract.TurnID, func()) (contract.TurnEnded, error) {
	panic("unexpected continuation")
}
func (h *harnessFake) Recover(id contract.TurnID, _ []contract.TurnInput) (contract.TurnEnded, bool, error) {
	h.mu.Lock()
	h.recoveries++
	h.mu.Unlock()
	return contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("completed"), Final: "recovered"}, true, nil
}
func (h *harnessFake) Steer(_ context.Context, input contract.TurnInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steers++
	h.delivered[input.Delivery] = true
	return nil
}
func (h *harnessFake) FollowUp(context.Context, contract.TurnInput) error {
	panic("unexpected follow-up")
}
func (h *harnessFake) Abort(context.Context) error { return nil }
func (h *harnessFake) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.delivered[id], nil
}
func (*harnessFake) Close() error { return nil }

type sinkFake struct {
	mu      sync.Mutex
	acked   map[contract.DeliverySeq]bool
	done    chan contract.TurnEnded
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (s *sinkFake) DeliveryDone(_ context.Context, id contract.DeliverySeq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked[id] = true
	return nil
}
func (s *sinkFake) TurnOutput(_ context.Context, result contract.TurnEnded) error {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first && s.entered != nil {
		close(s.entered)
		<-s.release
	}
	s.done <- result
	return nil
}

func TestRunnerReportsRepositorySetupFailure(t *testing.T) {
	sink := &sinkFake{done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Sink: sink, Setup: func(context.Context) error { return errors.New("cache unavailable") }}
	if err := runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a"}}); err != nil {
		t.Fatal(err)
	}
	got := <-sink.done
	if got.Turn != "turn_a" || got.Outcome != "failed" || got.Error != "repository setup: cache unavailable" {
		t.Fatal(got)
	}
}

func TestRunnerDoesNotRestartWhileOutputIsBlocked(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 2), entered: make(chan struct{}), release: make(chan struct{})}
	runner := &Runner{Harness: h, Sink: sink}
	start := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}
	if err := runner.Handle(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.entered
	err := runner.Handle(context.Background(), start)
	newTurn := contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_b", Inputs: []contract.TurnInput{{Delivery: 2}}}}
	nextErr := runner.Handle(context.Background(), newTurn)
	h.mu.Lock()
	recovered := h.recoveries
	h.mu.Unlock()
	close(sink.release)
	<-sink.done
	if err != nil || recovered != 0 || nextErr != errTurnSettling {
		t.Fatalf("replay error=%v recoveries=%d next error=%v", err, recovered, nextErr)
	}
}

func TestRunnerRecoversReplayedTurnWithDoneInput(t *testing.T) {
	h := &harnessFake{delivered: map[contract.DeliverySeq]bool{1: true}}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	if err := runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}}); err != nil {
		t.Fatal(err)
	}
	result := <-sink.done
	sink.mu.Lock()
	acked := sink.acked[1]
	sink.mu.Unlock()
	if !acked || result.Final != "recovered" {
		t.Fatalf("acked=%v result=%+v", acked, result)
	}
}

func TestRunnerWaitsForStartBeforeReadingSteer(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), permit: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	result := make(chan error, 1)
	go func() {
		result <- runner.Handle(context.Background(), contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1}}}})
	}()
	<-h.started
	select {
	case err := <-result:
		t.Fatalf("start returned before Pi accepted: %v", err)
	default:
	}
	close(h.permit)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.done
}

func TestRunnerSkipsReplayedSteer(t *testing.T) {
	h := &harnessFake{started: make(chan struct{}), release: make(chan struct{}), delivered: make(map[contract.DeliverySeq]bool)}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink}
	ctx := context.Background()
	if err := runner.Handle(ctx, contract.BoxMessage{Body: contract.TurnStart{Turn: "turn_a", Inputs: []contract.TurnInput{{Delivery: 1, Text: "start"}}}}); err != nil {
		t.Fatal(err)
	}
	<-h.started
	steer := contract.BoxMessage{Body: contract.TurnSteer{Input: contract.TurnInput{Delivery: 2, Text: "update"}, Queue: contract.QueueSteer}}
	if err := runner.Handle(ctx, steer); err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(ctx, steer); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	<-sink.done
	h.mu.Lock()
	count := h.steers
	h.mu.Unlock()
	sink.mu.Lock()
	acked := sink.acked[1] && sink.acked[2]
	sink.mu.Unlock()
	if count != 1 || !acked {
		t.Fatalf("steers=%d acked=%v", count, acked)
	}
}

func TestRunnerAppliesModelOnlyWhenItChanges(t *testing.T) {
	anthropic := contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "a", Thinking: contract.ThinkingLow}
	openai := contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "b", Thinking: contract.ThinkingLow}
	completed := fakeharness.Turn{Result: contract.TurnEnded{Outcome: "completed"}}
	h := &fakeharness.Harness{Turns: []fakeharness.Turn{completed, completed, completed}}
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 1)}
	runner := &Runner{Harness: h, Sink: sink, Model: anthropic}
	for i, model := range []contract.ModelChoice{anthropic, openai, openai} {
		start := contract.TurnStart{Turn: contract.TurnID(fmt.Sprint("turn_", i)), Inputs: []contract.TurnInput{{Delivery: contract.DeliverySeq(i + 1)}}, Model: model}
		if err := runner.Handle(context.Background(), contract.BoxMessage{Body: start}); err != nil {
			t.Fatal(err)
		}
		<-sink.done
	}
	if len(h.Models) != 1 || h.Models[0] != openai {
		t.Fatalf("SetModel calls: %+v", h.Models)
	}
}
