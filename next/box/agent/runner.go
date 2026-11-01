package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

var (
	errTurnSettling = errors.New("turn is settling")
	errStopped      = errors.New("stopped before Pi took the turn")
)

type turnSink interface {
	DeliveryDone(context.Context, contract.DeliverySeq) error
	TurnOutput(context.Context, contract.TurnEnded) error
}

// Runner forwards inputs to Pi without maintaining a second input queue.
type Runner struct {
	Harness harness.Harness
	Sink    turnSink
	Setup   func(context.Context) error
	// Attach stores an input's files in the box before Pi reads the input. It fails only when its context ends.
	Attach   func(context.Context, contract.TurnInput) (contract.TurnInput, error)
	setupErr error
	// What the last transfer applied. Both start empty, so a new Pi process runs its transfer on the first turn:
	// box.hello started Pi on the session's profile, and only Pi knows whether the conversation fits that model.
	model         contract.ModelChoice
	compactTokens int
	mu            sync.Mutex
	active        contract.TurnID
	settling      bool
	sent          map[contract.DeliverySeq]struct{}
	errors        chan error
	// A transfer or a download can take minutes, and a stop must reach it, so the channel keeps reading while it runs.
	ready        chan struct{}        // closes when Pi accepts the active turn
	transferring bool                 // Pi is preparing the turn and has not taken its inputs
	stopped      bool                 // a stop arrived while transferring
	holding      bool                 // steers wait in held: Pi has not accepted the turn, or one worker is sending them in order
	held         []contract.TurnSteer // in arrival order
	flushing     sync.WaitGroup
	attaching    context.Context // ends with the turn, so a download cannot outlive it
	stopAttach   context.CancelFunc
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
		if r.Setup != nil {
			r.setupErr = r.Setup(ctx)
			if r.setupErr != nil {
				return r.Sink.TurnOutput(ctx, contract.TurnEnded{Turn: m.Turn, Outcome: contract.TurnOutcome("failed"), Error: "repository setup: " + r.setupErr.Error(), Cause: contract.CauseSetupFailed})
			}
		}
		return r.start(ctx, m)
	case contract.TurnSteer:
		return r.steer(ctx, m)
	case contract.TurnStop:
		r.mu.Lock()
		active, transferring, ready := r.active == m.Turn, r.transferring, r.ready
		if active && transferring {
			r.stopped = true
			r.stopAttach()
		}
		r.mu.Unlock()
		if !active {
			return nil
		}
		if !transferring {
			// Pi can only stop a turn it has accepted.
			select {
			case <-ready:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return r.Harness.Abort(ctx)
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
	active, settling := r.active, r.settling
	r.mu.Unlock()
	if active != "" && active != start.Turn {
		if settling {
			return errTurnSettling
		}
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
	ready := make(chan struct{})
	attaching, stopAttach := context.WithCancel(ctx)
	r.mu.Lock()
	r.active = start.Turn
	r.ready = ready
	r.attaching, r.stopAttach = attaching, stopAttach
	r.holding = true
	r.sent = make(map[contract.DeliverySeq]struct{}, len(pending))
	for _, input := range pending {
		r.sent[input.Delivery] = struct{}{}
	}
	r.mu.Unlock()
	transferring := make(chan struct{})
	preparing := sync.OnceFunc(func() {
		r.mu.Lock()
		r.transferring = true
		r.mu.Unlock()
		close(transferring)
	})
	var readyOnce sync.Once
	started := func() { readyOnce.Do(func() { close(ready) }) }
	go func() {
		defer started()
		defer stopAttach()
		var result contract.TurnEnded
		var err error
		cause := contract.CauseHarnessFailed
		// A running turn keeps its model, and a plain reconnect changes nothing. The next turn applies a change, and a
		// compaction trigger is part of one because Pi took the old one as a process argument.
		transfer := func() error {
			if start.Model.ID == "" || (start.Model == r.model && start.CompactTokens == r.compactTokens) {
				return nil
			}
			preparing()
			moved, err := r.Harness.TransferModel(ctx, start.Turn, start.Model, start.CompactTokens)
			if errors.Is(err, harness.ErrRefused) {
				cause = contract.CauseSetModelFailed
			}
			if err != nil {
				return err
			}
			log.Printf("model transfer to %s: %s, %d to %d tokens. %s", start.Model.ID, moved.Path, moved.TokensBefore, moved.TokensAfter, moved.Reason)
			if moved.Path != harness.TransferAborted {
				r.model, r.compactTokens = start.Model, start.CompactTokens
			}
			return nil
		}
		attach := func() error {
			for i, input := range pending {
				if len(input.Files) == 0 {
					continue
				}
				preparing()
				var err error
				if pending[i], err = r.Attach(attaching, input); err != nil {
					return err
				}
			}
			return nil
		}
		prepare := func() error {
			attached := attach()
			r.mu.Lock()
			stopped := r.stopped
			r.mu.Unlock()
			if stopped {
				return errStopped
			}
			if attached != nil {
				return attached
			}
			if err := transfer(); err != nil {
				return err
			}
			if err := r.Harness.SetPromptSections(ctx, start.Sections); err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.transferring = false
			if r.stopped {
				return errStopped
			}
			return nil
		}
		accepted := func() {
			started()
			r.mu.Lock()
			defer r.mu.Unlock()
			r.flushing.Go(func() { r.flushHeld(ctx) })
		}
		if len(pending) > 0 {
			if err = prepare(); err == nil {
				result, err = r.Harness.Start(ctx, start.Turn, pending, accepted)
			}
		} else {
			var settled bool
			result, settled, err = r.Harness.Recover(start.Turn, start.Inputs)
			if err == nil && !settled {
				if err = prepare(); err == nil {
					result, err = r.Harness.Continue(ctx, start.Turn, accepted)
				}
			}
		}
		// Settling comes first, so no steer starts another flush while this one ends.
		r.mu.Lock()
		r.settling = true
		r.mu.Unlock()
		stopAttach()
		r.flushing.Wait()
		if errors.Is(err, errStopped) {
			// Pi never saw the inputs, so they wait for the next turn instead of counting as read.
			result = contract.TurnEnded{Turn: start.Turn, Outcome: contract.TurnOutcome("stopped"), Cause: contract.CauseNotStarted}
		} else if err != nil {
			result = contract.TurnEnded{Turn: start.Turn, Outcome: contract.TurnOutcome("failed"), Error: err.Error(), Cause: cause}
		}
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
	case <-transferring:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flushHeld sends the held steers in order, each once its files are in the box, and then stops holding. What the turn ends
// without sending stays in held, and finish hands it back.
func (r *Runner) flushHeld(ctx context.Context) {
	for {
		r.mu.Lock()
		if len(r.held) == 0 {
			r.holding = false
			r.mu.Unlock()
			return
		}
		next := r.held[0]
		attaching := r.attaching
		r.mu.Unlock()
		if len(next.Input.Files) > 0 {
			var err error
			if next.Input, err = r.Attach(attaching, next.Input); err != nil {
				return
			}
		}
		r.mu.Lock()
		r.held = r.held[1:]
		r.mu.Unlock()
		if err := r.forward(ctx, next); err != nil {
			select {
			case r.errors <- err:
			default:
			}
		}
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
	if r.holding {
		r.held = append(r.held, input)
		r.mu.Unlock()
		return nil
	}
	// A download can take minutes, so it runs off the channel, and later steers line up behind it.
	if len(input.Input.Files) > 0 {
		r.holding = true
		r.held = append(r.held, input)
		r.flushing.Go(func() { r.flushHeld(ctx) })
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	return r.forward(ctx, input)
}

func (r *Runner) forward(ctx context.Context, input contract.TurnSteer) error {
	var err error
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
	for _, left := range r.held {
		result.Unread = append(result.Unread, left.Input.Delivery)
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
	if err := r.Sink.TurnOutput(ctx, result); err != nil {
		return err
	}
	r.mu.Lock()
	r.active = ""
	r.settling = false
	r.sent = nil
	r.transferring, r.stopped, r.holding, r.held = false, false, false, nil
	r.mu.Unlock()
	return nil
}
