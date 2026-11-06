package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// removeBoxVolumes owns the whole physical volume phase, not a cached permission
// read before stopping a container. Both RPCs run before readiness can resume.
func (b *Boxes) removeBoxVolumes(ctx context.Context, name contract.HostName, h *podman.Host, id contract.BoxID) (bool, error) {
	return b.Store.RemoveBoxVolumes(ctx, id, name, func(ctx context.Context) error {
		err := h.RemoveVolume(ctx, id)
		if err == nil {
			err = h.RemoveStackVolumes(ctx, id)
		}
		var uncertain podman.RemoteCancellationUnconfirmed
		if !errors.As(err, &uncertain) {
			return err
		}

		// Preserve the caller's work/join budget and typed error. Physical ownership
		// outlives that caller: only a confirmed join releases its store fence.
		joined := make(chan struct{})
		retired := make(chan struct{})
		engineCtx := b.Context
		if engineCtx == nil {
			engineCtx = context.Background()
		}
		go func() {
			keeper, cancel := context.WithCancel(engineCtx)
			defer cancel()
			go func() {
				select {
				case <-b.Store.Done():
					cancel()
				case <-retired:
					cancel()
				case <-keeper.Done():
				}
			}()
			delay := b.retryDelay
			if delay == 0 {
				delay = time.Second
			}
			for keeper.Err() == nil {
				// Every Host command first joins all pending remote groups. Stop is
				// idempotent even when removal already succeeded, and creates no work.
				deadline := time.Now().Add(box.StartCleanupBudget)
				work, stop := context.WithDeadline(keeper, deadline.Add(-box.StartCleanupBudget/2))
				joinErr := h.StopContainer(podman.WithRemoteCancellationDeadline(work, deadline), id)
				stop()
				if joinErr == nil {
					close(joined)
					return
				}
				timer := time.NewTimer(delay)
				select {
				case <-keeper.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			// Shutdown is not confirmation. Keep the fence until the store closes;
			// no later readiness/dispatch may slip through while shutdown drains.
		}()
		return store.VolumeRemovalPending{Err: err, Joined: joined, Retired: retired}
	})
}
