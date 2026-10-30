package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

var errTurnSettling = errors.New("turn is settling")

type turnSink interface {
	DeliveryDone(context.Context, contract.DeliverySeq) error
	TurnOutput(context.Context, contract.TurnEnded) error
}

// Runner forwards inputs to Pi without maintaining a second input queue.
type Runner struct {
	Harness  harness.Harness
	Sink     turnSink
	mu       sync.Mutex
	active   contract.TurnID
	settling bool
	sent     map[contract.DeliverySeq]struct{}
	errors   chan error
}

func (r *Runner) Errors() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errors == nil {
		r.errors = make(chan error, 1)
	}
	return r.errors
}

func (r *Runner) Handle(ctx context.Context, message contract.BoxMessage) error {
	switch m := message.Body.(type) {
	case contract.TurnStart:
		return r.start(ctx, m)
	case contract.TurnSteer:
		return r.steer(ctx, m)
	case contract.TurnStop:
		r.mu.Lock()
		active := r.active == m.Turn
		r.mu.Unlock()
		if active {
			return r.Harness.Abort(ctx)
		}
		return nil
	case contract.Upgrade:
		return nil // Release skew is deferred to M2 (spec 5.10).
	default:
		return fmt.Errorf("unsupported box message")
	}
}

func (r *Runner) start(ctx context.Context, start contract.TurnStart) error {
	r.Errors()
	if len(start.Inputs) == 0 {
		return fmt.Errorf("turn has no inputs")
	}
	r.mu.Lock()
	active := r.active
	r.mu.Unlock()
	if active != "" && active != start.Turn {
		return fmt.Errorf("turn already active")
	}
	var pending []contract.TurnInput
	for _, input := range start.Inputs {
		delivered, err := r.Harness.Delivered(ctx, input.Delivery)
		if err != nil {
			return err
		}
		if delivered {
			if err := r.Sink.DeliveryDone(ctx, input.Delivery); err != nil {
				return err
			}
			continue
		}
		pending = append(pending, input)
	}
	if active != "" {
		return nil
	}
	r.mu.Lock()
	r.active = start.Turn
	r.sent = make(map[contract.DeliverySeq]struct{}, len(pending))
	for _, input := range pending {
		r.sent[input.Delivery] = struct{}{}
	}
	r.mu.Unlock()
	ready := make(chan struct{})
	var readyOnce sync.Once
	started := func() { readyOnce.Do(func() { close(ready) }) }
	go func() {
		defer started()
		var result contract.TurnEnded
		var err error
		if len(pending) > 0 {
			result, err = r.Harness.Start(ctx, start.Turn, pending, started)
		} else {
			var settled bool
			result, settled, err = r.Harness.Recover(start.Turn, start.Inputs)
			if err == nil && !settled {
				result, err = r.Harness.Continue(ctx, start.Turn, started)
			}
		}
		if err != nil {
			result = contract.TurnEnded{Turn: start.Turn, Outcome: contract.TurnOutcome("failed"), Error: err.Error()}
		}
		r.mu.Lock()
		r.settling = true
		r.mu.Unlock()
		if err := r.finish(ctx, result); err != nil {
			select {
			case r.errors <- err:
			default:
			}
		}
	}()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) steer(ctx context.Context, input contract.TurnSteer) error {
	delivered, err := r.Harness.Delivered(ctx, input.Input.Delivery)
	if err != nil {
		return err
	}
	if delivered {
		return r.Sink.DeliveryDone(ctx, input.Input.Delivery)
	}
	r.mu.Lock()
	if r.active == "" || r.settling {
		r.mu.Unlock()
		return errTurnSettling
	}
	if _, exists := r.sent[input.Input.Delivery]; exists {
		r.mu.Unlock()
		return nil
	}
	r.sent[input.Input.Delivery] = struct{}{}
	r.mu.Unlock()
	switch input.Queue {
	case contract.QueueSteer:
		err = r.Harness.Steer(ctx, input.Input)
	case contract.QueueFollowUp:
		err = r.Harness.FollowUp(ctx, input.Input)
	default:
		err = fmt.Errorf("unknown turn queue")
	}
	if err != nil {
		r.mu.Lock()
		delete(r.sent, input.Input.Delivery)
		r.mu.Unlock()
	}
	return err
}

func (r *Runner) finish(ctx context.Context, result contract.TurnEnded) error {
	r.mu.Lock()
	ids := make([]contract.DeliverySeq, 0, len(r.sent))
	for id := range r.sent {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		delivered, err := r.Harness.Delivered(ctx, id)
		if err != nil {
			return err
		}
		if delivered {
			if err := r.Sink.DeliveryDone(ctx, id); err != nil {
				return err
			}
		}
	}
	r.mu.Lock()
	r.active = ""
	r.settling = false
	r.sent = nil
	r.mu.Unlock()
	return r.Sink.TurnOutput(ctx, result)
}
