package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (r *Reconciler) closePass(ctx context.Context, row store.LifecycleBox, boxes []box.Box) error {
	for _, child := range boxes {
		if child.BudgetParent == row.ID && child.State != contract.BoxDeleted {
			return nil
		}
	}
	if row.State == contract.BoxDeleted {
		session, err := r.store.Session(ctx, row.Session)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		event, err := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: row.Session})
		if err != nil {
			return err
		}
		return r.store.CloseSession(ctx, row.Session, event)
	}
	if row.State != contract.BoxClosing {
		next := row.Box
		next.State, next.Reason = contract.BoxClosing, ""
		changed, err := r.change(ctx, row.Box, next, time.Time{}, row.Failures, "")
		if err != nil || !changed {
			return err
		}
		row.Box, row.DueAt = next, time.Time{}
	}
	if !row.DueAt.IsZero() && row.DueAt.After(r.now()) {
		return nil
	}
	hadTurns, err := r.store.SessionHasTurns(ctx, row.Session)
	if err != nil {
		return err
	}
	if row.Want == store.WantClose && hadTurns && r.view.Connected(row.Session) {
		return nil
	}
	r.launch(ctx, row)
	return nil
}

func (r *Reconciler) closeStep(ctx context.Context, row store.LifecycleBox) error {
	hadTurns, err := r.store.SessionHasTurns(ctx, row.Session)
	if err != nil {
		return err
	}
	next := row.Box
	var cause contract.FailureCause
	if row.Want == store.WantDiscard || !hadTurns {
		if err = r.boxes.Remove(ctx, row.ID); err != nil {
			next.State, next.Reason, cause = contract.BoxFailed, err.Error(), contract.CauseRemoveFailed
		} else {
			next.State, next.Reason = contract.BoxDeleted, ""
		}
	} else {
		err = r.boxes.Start(ctx, row.ID)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if retryErr := r.store.SetArchiveRetry(ctx, row.ID, r.now().Add(30*time.Second)); retryErr != nil {
			return retryErr
		}
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, changeErr := r.change(ctx, row.Box, next, time.Time{}, row.Failures, cause)
	return changeErr
}
