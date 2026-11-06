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

func (b *Boxes) startStep(ctx context.Context, spec lifecycle.BoxSpec, step contract.StartStep, work func(context.Context) error) error {
	b.mu.Lock()
	if b.starts == nil {
		b.starts = make(map[contract.BoxID]contract.BoxStartStatus)
	}
	b.starts[spec.Box] = contract.BoxStartStatus{Host: spec.Host, Step: step, StartedAt: time.Now()}
	b.mu.Unlock()
	budget := box.StartStepBudget
	if step == contract.StartImage || step == contract.StartInstructions {
		budget = box.StartPreparationBudget
	}
	if step == contract.StartBinary {
		err := work(ctx)
		var unconfirmed podman.RemoteCancellationUnconfirmed
		if errors.Is(err, podman.ErrTransferIdle) {
			timeout := box.StepTimeout{Host: spec.Host, Step: step}
			if errors.As(err, &unconfirmed) {
				timeout.Err = unconfirmed
			}
			return timeout
		}
		var missing podman.BinaryDecompressorMissing
		if errors.As(err, &missing) || errors.As(err, &unconfirmed) {
			return box.RuntimeCreateError{Err: err}
		}
		return err
	}
	bound := b.stepContext
	if bound == nil {
		bound = context.WithTimeout
	}
	child, cancel := bound(ctx, budget)
	defer cancel()
	err := work(podman.WithRemoteCancellation(child))
	if ctx.Err() == nil && errors.Is(child.Err(), context.DeadlineExceeded) {
		if step == contract.StartImage {
			return box.ImageCreateError{Err: errors.Join(child.Err(), err)}
		}
		timeout := box.StepTimeout{Host: spec.Host, Step: step}
		var unconfirmed podman.RemoteCancellationUnconfirmed
		if errors.As(err, &unconfirmed) {
			timeout.Err = unconfirmed
		}
		return timeout
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	var unconfirmed podman.RemoteCancellationUnconfirmed
	if errors.As(err, &unconfirmed) {
		return box.RuntimeCreateError{Err: err}
	}
	return err
}

// Leave half the cleanup budget for independently killing/joining an uncertain remote frame, without extending the
// overall deadline. Work has no parent cancellation, since cleanup is owned even after the original start is stopped.
func startCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(box.StartCleanupBudget)
	work, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline.Add(-box.StartCleanupBudget/2))
	return podman.WithRemoteCancellationDeadline(work, deadline), cancel
}

// Keep podman's cancellation contract visible at this boundary: startStep does not orphan an unjoined goroutine to enforce a
// deadline. Production ExecRunner kills and joins SSH and bounds pipe draining with WaitDelay.
var _ podman.ProgressRunner = podman.ExecRunner{}
