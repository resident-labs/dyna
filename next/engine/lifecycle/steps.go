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
		if workErr != nil || found && status.Running && !r.view.Connected(b.Session) {
			due = r.now().Add(60 * time.Second)
		} else if found && status.Running {
			next.State, next.Reason = contract.BoxReady, ""
		} else if found {
			next.State, next.Reason = contract.BoxStopped, "container stopped"
			if row.Want == store.WantRun && b.Reason == boxDisconnectedReason {
				failures++
				if failures >= 3 {
					next.State, cause = contract.BoxFailed, contract.CauseStartFailed
				} else if failures > 1 {
					// The first crash restarts at once; the 60 s disconnect wait already passed.
					due = r.retryAt(failures)
				}
			}
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
		steps := box.NewStepLog("box " + string(b.ID))
		if !fresh {
			workErr = r.boxes.Remove(ctx, b.ID)
			steps.Done("remove old")
		}
		var sources []contract.BoxRepo
		if workErr == nil && repos != nil {
			sources, workErr = repos.Get(ctx, b.Session, b.Host, true)
			steps.Done("repositories")
		}
		if workErr == nil {
			next.Image, workErr = r.boxes.Create(ctx, BoxSpec{Box: b.ID, Session: b.Session, Room: b.Room, Host: b.Host, Slice: b.Slice, Size: b.Size, Repos: sources})
			steps.Done("create")
		}
		var missing box.MissingDevice
		if errors.As(workErr, &missing) {
			next.State, next.Reason = contract.BoxFailed, missing.Error()
			if missing.Need == box.NeedKVM {
				cause = contract.CauseKVMDeviceMissing
			} else {
				cause = contract.CauseNestedDeviceMissing
			}
		} else if workErr != nil {
			failures++
			if failures >= 3 {
				next.State, next.Reason = contract.BoxFailed, string(contract.CauseCreateFailed)
				cause = contract.CauseCreateFailed
				var stack box.StackError
				if errors.As(workErr, &stack) {
					next.Reason = stack.Error()
				}
			} else {
				next.State, next.Reason = contract.BoxPending, string(box.PendingCreateRetry)
				due = r.retryAt(failures)
			}
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
		slice, err := box.SliceName(b.ID)
		if err != nil {
			return err
		}
		status, found, inspectErr := r.boxes.Inspect(ctx, b.ID)
		image, imageErr := r.boxes.CurrentImage(ctx, b.Session, b.Host)
		if imageErr != nil {
			next.Reason = imageErr.Error()
		}
		if inspectErr != nil {
			workErr = inspectErr
		} else if imageErr != nil {
			// A box must not start on an old image: its Pi may not match the box agent, which comes from the host.
			workErr = imageErr
		} else if stale := image.Tag != b.Image || status.Stack != image.StackHash(); slice != b.Slice || stale || !found || status.StaleLayout {
			r.mu.Lock()
			repos := r.repos
			r.mu.Unlock()
			var sources []contract.BoxRepo
			if repos != nil {
				sources, workErr = repos.Get(ctx, b.Session, b.Host, false)
			}
			if workErr == nil {
				workErr = r.boxes.Recreate(ctx, BoxSpec{Box: b.ID, Session: b.Session, Room: b.Room, Host: b.Host, Slice: slice, Size: b.Size, Repos: sources, Needs: image.Needs, Stack: image.Stack}, image.Tag, b.Slice)
			}
			if workErr == nil {
				var notice *contract.Event
				if stale {
					var event contract.Event
					event, workErr = r.event(b.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionRestarted{Session: b.Session, ImageUpdated: true})
					notice = &event
				}
				if workErr == nil {
					workErr = r.store.SetRecreated(ctx, b.ID, slice, image.Tag, notice)
					next.Image, next.Slice = image.Tag, slice
				}
			}
		} else {
			workErr = r.boxes.Start(ctx, b.ID)
		}
		var missing box.MissingDevice
		if errors.As(workErr, &missing) {
			next.State, next.Reason = contract.BoxFailed, missing.Error()
			if missing.Need == box.NeedKVM {
				cause = contract.CauseKVMDeviceMissing
			} else {
				cause = contract.CauseNestedDeviceMissing
			}
		} else if workErr != nil {
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
