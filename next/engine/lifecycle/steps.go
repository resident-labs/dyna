package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (r *Reconciler) retryAt(failures int) time.Time {
	delay := 30 * time.Second
	for i := 1; i < failures && delay < 10*time.Minute; i++ {
		delay *= 2
	}
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	return r.now().Add(delay)
}

func (r *Reconciler) step(ctx context.Context, row store.LifecycleBox) error {
	rows, err := r.store.LifecycleBoxes(ctx)
	if err != nil {
		return err
	}
	current := false
	for _, saved := range rows {
		if saved.ID == row.ID && saved.State == row.State && saved.Want == row.Want && saved.DueAt.Equal(row.DueAt) {
			current = true
			break
		}
	}
	if !current {
		return nil
	}
	if row.Want == store.WantClose || row.Want == store.WantDiscard {
		return r.closeStep(ctx, row)
	}
	b := row.Box
	next := b
	var due time.Time
	failures := row.Failures
	var cause contract.FailureCause
	var workErr error
	switch {
	case b.State == contract.BoxLost:
		status, found, inspectErr := r.boxes.Inspect(ctx, b.ID)
		workErr = inspectErr
		if workErr != nil {
			due = r.now().Add(60 * time.Second)
		} else if found && status.Running {
			if r.view.Connected(b.Session) {
				next.State, next.Reason = contract.BoxReady, ""
			} else {
				due = r.now().Add(60 * time.Second)
			}
		} else if found {
			next.State, next.Reason = contract.BoxStopped, "container stopped"
		} else {
			next.State, next.Reason = contract.BoxFailed, "container missing"
			cause = contract.CauseHostLost
		}
	case row.Want == store.WantStop:
		// A terminal admitted after this check ends with the box. WakeTerminal already set
		// want=run, so the box restarts; serializing admission would bring back the per-session lock.
		if r.view.OpenTerminal(b.Session) {
			return r.store.WakeTerminal(ctx, b.ID)
		}
		workErr = r.boxes.Stop(ctx, b.ID)
		if workErr == nil {
			next.State = contract.BoxStopped
		} else {
			failures++
			due = r.retryAt(failures)
		}
	case b.State == contract.BoxCreating:
		r.mu.Lock()
		fresh := r.fresh[b.ID]
		delete(r.fresh, b.ID)
		repos := r.repos
		r.mu.Unlock()
		if !fresh {
			workErr = r.boxes.Remove(ctx, b.ID)
		}
		var sources []contract.BoxRepo
		if workErr == nil && repos != nil {
			sources, workErr = repos.Get(ctx, b.Session, b.Host, true)
		}
		if workErr == nil {
			next.Image, workErr = r.boxes.Create(ctx, BoxSpec{Box: b.ID, Session: b.Session, Host: b.Host, Slice: b.Slice, Size: b.Size, Repos: sources})
		}
		if workErr != nil {
			next.State, next.Reason = contract.BoxFailed, workErr.Error()
			cause = contract.CauseCreateFailed
		} else {
			next.State = contract.BoxStarting
			due = r.now().Add(60 * time.Second)
		}
	case !row.DueAt.IsZero():
		hadTurns, err := r.store.SessionHasTurns(ctx, b.Session)
		if err != nil {
			return err
		}
		if hadTurns {
			workErr = r.boxes.Stop(ctx, b.ID)
			if workErr == nil {
				next.State, next.Reason = contract.BoxStopped, "no hello within 60 s"
				failures++
				due = r.retryAt(failures)
				cause = contract.CauseNoHello
				if failures >= 3 {
					cause = contract.CauseStartFailed
				}
			}
		} else {
			workErr = r.boxes.Remove(ctx, b.ID)
			if workErr == nil {
				next.State = contract.BoxFailed
				next.Reason = "no hello within 60 s"
				cause = contract.CauseNoHello
			}
		}
		if workErr != nil {
			failures++
			due = r.retryAt(failures)
		}
	default:
		path, err := r.store.BoxAncestry(ctx, b.ID)
		if err != nil {
			return err
		}
		ids := make([]contract.BoxID, len(path))
		for i := range path {
			ids[i] = path[i].ID
		}
		slice, err := box.SliceName(ids)
		if err != nil {
			return err
		}
		if slice != b.Slice {
			r.mu.Lock()
			repos := r.repos
			r.mu.Unlock()
			var sources []contract.BoxRepo
			if repos != nil {
				sources, workErr = repos.Get(ctx, b.Session, b.Host, false)
			}
			if workErr == nil {
				workErr = r.boxes.Recreate(ctx, BoxSpec{Box: b.ID, Session: b.Session, Host: b.Host, Slice: slice, Size: b.Size, Repos: sources}, b.Image, b.Slice)
			}
			if workErr == nil {
				workErr = r.store.SetRecreated(ctx, b.ID, slice)
				next.Slice = slice
			}
		} else {
			workErr = r.boxes.Start(ctx, b.ID)
		}
		if workErr != nil {
			next.State = contract.BoxStopped
			failures++
			due = r.retryAt(failures)
			if failures >= 3 {
				next.Reason = workErr.Error()
				cause = contract.CauseStartFailed
			}
		} else {
			due = r.now().Add(60 * time.Second)
		}
	}
	if ctx.Err() != nil {
		if row.Want != store.WantStop || workErr != nil {
			return ctx.Err()
		}
		// A successful stop is an observed fact even if close cancelled it.
		ctx = context.WithoutCancel(ctx)
	}
	_, err = r.change(ctx, b, next, due, failures, cause)
	return errors.Join(err, workErr)
}
