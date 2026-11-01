package lifecycle

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// archiveRetry spaces out tries on a box whose archive failed, as a full disk makes it do.
const archiveRetry = 10 * time.Minute

// closeNotConnected marks a box that started for archive and never connected, so it shows as stuck rather than running.
const closeNotConnected = "box did not connect to archive"

func (r *Reconciler) closePass(ctx context.Context, row store.LifecycleBox, host box.HostState) error {
	leaf, err := r.store.ClosingLeaf(ctx, row.Session)
	if err != nil || !leaf {
		return err
	}
	if row.State == contract.BoxDeleted {
		session, found, err := r.store.OpenSession(ctx, row.Session)
		if err != nil || !found {
			return err
		}
		event, err := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: row.Session, Reason: session.CloseReason})
		if err != nil {
			return err
		}
		if err := r.store.CloseSession(ctx, row.Session, event); err != nil {
			return err
		}
		r.view.SessionClosed(row.Session)
		r.Kick()
		return nil
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
	if row.Deleting && r.roomHosts != nil && row.Host != "" && host != box.HostReady {
		return r.abandon(ctx, row)
	}
	if row.Reason == closeNotConnected && r.view.Connected(row.Session) {
		return r.retryArchive(ctx, row) // It was only slow.
	}
	if !row.DueAt.IsZero() && row.DueAt.After(r.now()) {
		return nil
	}
	hadTurns, err := r.store.SessionHasTurns(ctx, row.Session)
	if err != nil {
		return err
	}
	if row.Want == store.WantClose && hadTurns && r.view.Connected(row.Session) {
		if row.Reason == "" || row.Updated.Add(archiveRetry).After(r.now()) {
			return nil
		}
		return r.retryArchive(ctx, row)
	}
	r.launch(ctx, row)
	return nil
}

// retryArchive clears a failed archive, so the stream sends it again. The box keeps its volume until a ref is verified.
func (r *Reconciler) retryArchive(ctx context.Context, row store.LifecycleBox) error {
	session, err := r.store.Session(ctx, row.Session)
	if err != nil {
		return err
	}
	event, err := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: row.ID, Session: row.Session, Host: row.Host, From: contract.BoxClosing, To: contract.BoxClosing})
	if err != nil {
		return err
	}
	return r.store.SetClosingReason(ctx, row.ID, "", event)
}

func (r *Reconciler) closeStep(ctx context.Context, row store.LifecycleBox) error {
	hadTurns, err := r.store.SessionHasTurns(ctx, row.Session)
	if err != nil {
		return err
	}
	if row.Want == store.WantDiscard || !hadTurns {
		if err = r.boxes.Remove(ctx, row.ID); err == nil {
			next := row.Box
			next.State, next.Reason = contract.BoxDeleted, ""
			_, err = r.change(ctx, row.Box, next, time.Time{}, row.Failures, "")
			return err
		}
	} else {
		err = r.boxes.Start(ctx, row.ID)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if !row.DueAt.IsZero() && (row.Reason == "" || row.Reason == closeNotConnected) {
		reason = closeNotConnected // An earlier start worked and the box still has not connected.
	}
	retry := 30 * time.Second
	if reason != "" && row.Reason != "" {
		retry = archiveRetry
	}
	session, eventErr := r.store.Session(ctx, row.Session)
	if eventErr != nil {
		return eventErr
	}
	event, eventErr := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: row.ID, Session: row.Session, Host: row.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason})
	if eventErr != nil {
		return eventErr
	}
	if retryErr := r.store.SetArchiveRetry(ctx, row.ID, row.Want, r.now().Add(retry), reason, event); retryErr != nil {
		return retryErr
	}
	return err
}
