package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type harnessFake struct {
	mu        sync.Mutex
	started   chan struct{}
	release   chan struct{}
	permit    chan struct{}
	steers    int
	delivered map[contract.DeliverySeq]bool
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
func (h *harnessFake) Continue(context.Context, contract.TurnID, func()) (contract.TurnEnded, error) {
	panic("unexpected continuation")
}
func (h *harnessFake) Recover(contract.TurnID, []contract.TurnInput) (contract.TurnEnded, bool, error) {
	panic("unexpected recovery")
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
	mu    sync.Mutex
	acked map[contract.DeliverySeq]bool
	done  chan contract.TurnEnded
}

func (s *sinkFake) DeliveryDone(_ context.Context, id contract.DeliverySeq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked[id] = true
	return nil
}
func (s *sinkFake) TurnOutput(_ context.Context, result contract.TurnEnded) error {
	s.done <- result
	return nil
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
