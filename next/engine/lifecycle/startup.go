package lifecycle

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// FailStaleCreates runs once before restoring box sockets after an engine start.
func (r *Reconciler) FailStaleCreates(ctx context.Context) error {
	rows, err := r.store.LifecycleBoxes(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.State != contract.BoxCreating || !row.Updated.Before(r.now().Add(-15*time.Minute)) {
			continue
		}
		next := row.Box
		next.State, next.Reason = contract.BoxFailed, string(contract.CauseCreateFailed)
		event, err := r.event(row.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Cause: contract.CauseCreateFailed, Phase: contract.CreatePhaseBox})
		if err != nil {
			return err
		}
		changed, err := r.store.SetBoxLifecycle(ctx, row.Box, next, time.Time{}, row.Failures, event)
		if err != nil {
			return err
		}
		if changed && row.Host != "" {
			// The attempt may have left resources on the host: owe their removal to the existing sweep.
			if err := r.store.AddBoxLeftover(ctx, row.Host, row.Room, row.ID, row.Slice); err != nil {
				return err
			}
		}
	}
	return nil
}
