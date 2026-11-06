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
// read before stopping a container. Once the fresh proof claims named work,
// container, pod, workspace and stack deletion run in one locked host command.
func (b *Boxes) removeBoxVolumes(ctx context.Context, name contract.HostName, h *podman.Host, record box.Box) (bool, error) {
	id := record.ID
	epoch, err := h.BoxEpoch(ctx, id)
	if err != nil {
		return false, err
	}
	// Preserve the actual Stop boundary: a wake/ready/turn before this join
	// completes must be visible to the fresh proof, not inherit a prior boolean.
	if err := h.StopBox(ctx, id, epoch); err != nil {
		var superseded podman.NamespaceSuperseded
		if errors.As(err, &superseded) {
			return true, err
		}
		return false, err
	}
	supersededDelete := false
	removed, err := b.Store.RemoveBoxVolumes(ctx, id, name, func(ctx context.Context) error {
		err := removeHostBoxAtEpoch(ctx, h, record, b.forward(id), epoch, true)
		var superseded podman.NamespaceSuperseded
		if errors.As(err, &superseded) {
			supersededDelete = true
			return nil
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
				// Every Host command first joins all pending remote groups. Read
				// only: a keeper must never stop a newer host epoch.
				deadline := time.Now().Add(box.StartCleanupBudget)
				work, stop := context.WithDeadline(keeper, deadline.Add(-box.StartCleanupBudget/2))
				_, joinErr := h.BoxEpoch(podman.WithRemoteCancellationDeadline(work, deadline), id)
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
	if supersededDelete {
		return true, box.NamespaceSuperseded{Box: id}
	}
	if err != nil || removed {
		return removed, err
	}
	// Policy refused named-volume deletion. Remove runnable resources only, with
	// the SAME captured epoch; never refresh an old deletion's authority.
	err = removeHostBoxAtEpoch(ctx, h, record, b.forward(id), epoch, false)
	var superseded podman.NamespaceSuperseded
	if errors.As(err, &superseded) {
		return true, err
	}
	return false, err
}
