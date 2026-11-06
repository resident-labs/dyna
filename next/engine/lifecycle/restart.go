package lifecycle

import (
	"context"
	"fmt"

	"github.com/resident-labs/dyna/contract"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/engine/store"
)

// QuiesceRestart claims a failed box's names against admission, lifecycle work and
// leftover repayment. The caller must recheck its failed snapshot after the join,
// and release after cleanup and the reset commit. It grants no deletion authority.
// Only the short claim/release holds pass; unrelated boxes keep progressing while
// the old box's workers or host take time. Concurrent restart claims refuse.
func (r *Reconciler) QuiesceRestart(ctx context.Context, old box.Box) (release func(), claimed bool, err error) {
	r.pass.Lock()
	if r.quiesced[old.ID] {
		r.pass.Unlock()
		return nil, false, nil
	}
	debts, err := r.store.BoxLeftovers(ctx, old.ID)
	if err != nil {
		r.pass.Unlock()
		return nil, false, err
	}
	r.quiesced[old.ID] = true
	var joins []struct {
		done <-chan struct{}
		host contract.HostName
	}
	join := func(done <-chan struct{}, host contract.HostName) {
		joins = append(joins, struct {
			done <-chan struct{}
			host contract.HostName
		}{done, host})
	}
	r.mu.Lock()
	if step := r.active[old.ID]; step != nil {
		// Stop/close already own their cleanup. Join them without cancelling it;
		// the caller's fresh-state check will observe their newer intent.
		if step.want == store.WantRun {
			step.cancel()
		}
		join(step.done, old.Host)
	}
	join(r.roomSteps["host "+string(old.Host)], old.Host)
	for _, debt := range debts {
		join(r.roomSteps["host "+string(debt.Host)], debt.Host)
	}
	r.mu.Unlock()
	r.pass.Unlock()
	release = func() {
		r.pass.Lock()
		delete(r.quiesced, old.ID)
		r.pass.Unlock()
		r.Kick()
	}
	for _, pending := range joins {
		if pending.done == nil {
			continue
		}
		select {
		case <-pending.done:
		case <-ctx.Done():
			release()
			return nil, false, fmt.Errorf("restart join on host %s: %w", pending.host, ctx.Err())
		}
	}
	// A bounded Stop/debt caller can have returned while its remote deletion
	// group is still unconfirmed. Join that physical owner too, without pass or
	// writer locks. The store reports the actual current/prior keeper hosts.
	if err := r.store.JoinBoxVolumeRemovals(ctx, old.ID); err != nil {
		release()
		return nil, false, err
	}
	// All old workers are joined and the ID remains fenced. Their completion
	// cache cannot cover resources a subsequent incarnation creates under these names.
	r.mu.Lock()
	for debt := range r.retained {
		if debt.Box == old.ID {
			delete(r.retained, debt)
		}
	}
	r.mu.Unlock()
	return release, true, nil
}

// FailedBoxRemover is the explicit destructive runtime operation used only by an
// authorized owner restart after QuiesceRestart and a fresh failed-state check.
// Unlike ordinary recovery Remove, it deletes workspace and stack volumes too.
// The snapshot's old host is used before the store can detach it.
type FailedBoxRemover interface {
	RemoveFailedBox(context.Context, box.Box) error
}
