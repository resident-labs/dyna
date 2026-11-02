// Package fakeharness provides a controlled harness for callers' unit tests.
package fakeharness

import (
	"context"
	"errors"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

// ToolCall is a scripted tool action observed by the test.
type ToolCall struct {
	Name   string
	Input  string
	Output string
	Failed bool
}

// Turn supplies a result after Release is closed. The caller controls when a turn settles.
type Turn struct {
	Result  contract.TurnEnded
	Err     error
	Release <-chan struct{}
	Tools   []ToolCall
}

type Input struct {
	TurnInput contract.TurnInput
	Queue     contract.TurnQueue
}

// Harness records inputs and runs the supplied turns in order.
type Harness struct {
	mu       sync.Mutex
	Turns    []Turn
	Inputs   []Input
	OnTool   func(ToolCall)
	Models   []contract.ModelChoice // each TransferModel call, in order
	Triggers []int                  // the compaction trigger of each TransferModel call
	// Transfer stands in for Pi's transfer, if set. It can block to hold a transfer open.
	Transfer func(context.Context) (harness.ModelTransfer, error)
	// CompactFunc stands in for Pi's compaction, if set. It can block to hold one open, and it gets the size asked for.
	CompactFunc func(context.Context, int64) (harness.Compaction, error)
	Compacts    int // each Compact call
	delivered   map[contract.DeliverySeq]bool
	stopped     chan struct{}
}

var _ harness.Harness = (*Harness)(nil)

func (h *Harness) Start(ctx context.Context, id contract.TurnID, inputs []contract.TurnInput, started func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	if len(h.Turns) == 0 {
		h.mu.Unlock()
		return contract.TurnEnded{}, errors.New("fake harness: no scripted turn")
	}
	turn := h.Turns[0]
	h.Turns = h.Turns[1:]
	if h.delivered == nil {
		h.delivered = make(map[contract.DeliverySeq]bool)
	}
	for _, input := range inputs {
		h.Inputs = append(h.Inputs, Input{TurnInput: input})
		h.delivered[input.Delivery] = true
	}
	stop := make(chan struct{})
	h.stopped = stop
	h.mu.Unlock()
	started()
	for _, tool := range turn.Tools {
		if h.OnTool != nil {
			h.OnTool(tool)
		}
	}
	if turn.Release != nil {
		select {
		case <-turn.Release:
		case <-stop:
			return contract.TurnEnded{Turn: id, Outcome: contract.TurnOutcome("stopped")}, nil
		case <-ctx.Done():
			return contract.TurnEnded{}, ctx.Err()
		}
	}
	result := turn.Result
	result.Turn = id
	return result, turn.Err
}

func (h *Harness) TransferModel(ctx context.Context, _ contract.TurnID, model contract.ModelChoice, compactTokens int) (harness.ModelTransfer, error) {
	h.mu.Lock()
	h.Models = append(h.Models, model)
	h.Triggers = append(h.Triggers, compactTokens)
	transfer := h.Transfer
	h.mu.Unlock()
	if transfer != nil {
		return transfer(ctx)
	}
	return harness.ModelTransfer{Path: "switched"}, nil
}

func (h *Harness) Compact(ctx context.Context, over int64) (harness.Compaction, error) {
	h.mu.Lock()
	h.Compacts++
	compact := h.CompactFunc
	h.mu.Unlock()
	if compact != nil {
		return compact(ctx, over)
	}
	return harness.Compaction{}, nil
}

func (*Harness) SetPromptSections(context.Context, []contract.ContextSection) error { return nil }

func (h *Harness) Continue(ctx context.Context, id contract.TurnID, started func()) (contract.TurnEnded, error) {
	return h.Start(ctx, id, nil, started)
}

func (h *Harness) Recover(id contract.TurnID, _ []contract.TurnInput) (contract.TurnEnded, bool, error) {
	return contract.TurnEnded{Turn: id}, false, nil
}

func (h *Harness) Steer(_ context.Context, input contract.TurnInput) error {
	return h.queue(input, contract.QueueSteer)
}

func (h *Harness) FollowUp(_ context.Context, input contract.TurnInput) error {
	return h.queue(input, contract.QueueFollowUp)
}

func (h *Harness) queue(input contract.TurnInput, queue contract.TurnQueue) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Inputs = append(h.Inputs, Input{TurnInput: input, Queue: queue})
	return nil
}

// Consume records a queued input only when the scripted harness actually takes it.
func (h *Harness) Consume(id contract.DeliverySeq) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.delivered == nil {
		h.delivered = make(map[contract.DeliverySeq]bool)
	}
	h.delivered[id] = true
}

func (h *Harness) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.delivered[id], nil
}

func (h *Harness) Abort(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped != nil {
		close(h.stopped)
		h.stopped = nil
	}
	return nil
}

func (*Harness) Close() error { return nil }
