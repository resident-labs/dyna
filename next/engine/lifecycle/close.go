package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// closeRetry spaces out tries to remove a box that would not go.
const closeRetry = 30 * time.Second

// diffWait is how long a connected box has to save its diff (spec 5.9) before close removes it all the same.
const diffWait = 2 * time.Minute

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
		engine := contract.Principal{Kind: contract.PrincipalEngine}
		if row.Want == store.WantClose && session.Repo != "" {
			// A box that reported its diff sent this notice already, under the same client ID, and a second one is dropped.
			tree, err := r.store.RoomTree(ctx, session.Room)
			if err != nil {
				return err
			}
			notice, err := r.event(session.Room, engine, contract.SessionNotice{Session: row.Session, Text: (*contract.DiffSavedRequest)(nil).CloseNotice(session.Name), ClientID: "close-diff:" + string(row.ID)})
			if err != nil {
				return err
			}
			if err := r.store.SendSessionNotice(ctx, notice, tree.Parent(row.Session)); err != nil {
				return err
			}
		}
		event, err := r.event(session.Room, engine, contract.SessionClosed{Session: row.Session, Reason: session.CloseReason})
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
		hadTurns, err := r.store.SessionHasTurns(ctx, row.Session)
		if err != nil {
			return err
		}
		var due time.Time
		if row.Want == store.WantClose && hadTurns && r.view.Connected(row.Session) {
			due = r.now().Add(diffWait) // The box saves its diff first; its report brings the removal sooner.
		}
		changed, err := r.change(ctx, row.Box, next, due, 0, "", "", false)
		if err != nil || !changed {
			return err
		}
		row.Box, row.DueAt, row.Failures = next, due, 0
	}
	if (row.Want == store.WantDiscard || row.Want == store.WantClose) && r.roomHosts != nil && row.Host != "" && host != box.HostReady {
		return r.abandon(ctx, row)
	}
	if !row.DueAt.IsZero() && row.DueAt.After(r.now()) {
		return nil
	}
	r.launch(ctx, row)
	return nil
}

func (r *Reconciler) closeStep(ctx context.Context, row store.LifecycleBox) error {
	err := r.boxes.Remove(ctx, row.ID)
	if err == nil {
		next := row.Box
		next.State, next.Reason = contract.BoxDeleted, ""
		_, err = r.change(ctx, row.Box, next, time.Time{}, row.Failures, "", "", false)
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	reason := err.Error()
	session, eventErr := r.store.Session(ctx, row.Session)
	if eventErr != nil {
		return eventErr
	}
	event, eventErr := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: row.ID, Session: row.Session, Host: row.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason})
	if eventErr != nil {
		return eventErr
	}
	if retryErr := r.store.SetCloseRetry(ctx, row.ID, row.Want, r.now().Add(closeRetry), reason, event); retryErr != nil {
		return retryErr
	}
	if row.Deleting {
		if reasonErr := r.store.SetRoomDeleteReason(ctx, row.Room, fmt.Sprintf("delete room %s: box %s: %s", row.Room, row.ID, reason)); reasonErr != nil {
			return reasonErr
		}
	}
	return err
}
