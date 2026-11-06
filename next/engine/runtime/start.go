package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type binaryInstall struct {
	done     chan struct{}
	cancel   context.CancelFunc
	progress chan struct{}
	refs     int   // Boxes.mu
	closing  bool  // the last subscriber is joining cancellation
	err      error // published by done
}

func (b *Boxes) HostHealth(name contract.HostName) contract.HostHealth {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loads[name].summary.Health
}

func (b *Boxes) StartStatus(id contract.BoxID) *contract.BoxStartStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.starts[id]
	if !ok {
		return nil
	}
	return &s
}

// startStep bounds local host operations. Binary transfers have their own byte-progress idle budget.
// This does not add remote process ownership or change Stop, restart, or failed-create cleanup semantics.
func (b *Boxes) startStep(ctx context.Context, spec lifecycle.BoxSpec, step contract.StartStep, work func(context.Context) error) error {
	b.mu.Lock()
	if b.starts == nil {
		b.starts = make(map[contract.BoxID]contract.BoxStartStatus)
	}
	b.starts[spec.Box] = contract.BoxStartStatus{Host: spec.Host, Step: step, StartedAt: time.Now()}
	b.mu.Unlock()
	if step == contract.StartBinary {
		err := work(ctx)
		if errors.Is(err, podman.ErrTransferIdle) {
			return box.StepTimeout{Host: spec.Host, Step: step}
		}
		var missing podman.BinaryDecompressorMissing
		if errors.As(err, &missing) {
			return box.RuntimeCreateError{Err: err}
		}
		return err
	}
	budget := box.StartStepBudget
	if step == contract.StartImage || step == contract.StartInstructions {
		budget = box.StartPreparationBudget
	}
	bound := b.stepContext
	if bound == nil {
		bound = context.WithTimeout
	}
	child, cancel := bound(ctx, budget)
	defer cancel()
	err := work(child)
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	if errors.Is(child.Err(), context.DeadlineExceeded) {
		if step == contract.StartImage {
			return box.ImageCreateError{Err: errors.Join(child.Err(), err)}
		}
		return box.StepTimeout{Host: spec.Host, Step: step}
	}
	return err
}
