package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// archiveRetry spaces out tries on a box whose archive failed, as a full disk makes it do.
const archiveRetry = 10 * time.Minute

// archiveAttempts is how many failed archive reports end the retries, so a close that cannot archive does not hold a running
// box for hours. Five reports ten minutes apart, each at most 15 minutes in the box, take under two hours. The box then stops
// and keeps its volume, because the engine never discards unasked.
const archiveAttempts = 5

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
		changed, err := r.change(ctx, row.Box, next, time.Time{}, 0, "") // From here failures counts failed archives.
		if err != nil || !changed {
			return err
		}
		row.Box, row.DueAt, row.Failures = next, time.Time{}, 0
	}
	if row.Want == store.WantDiscard && r.roomHosts != nil && row.Host != "" && host != box.HostReady {
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
		if row.Reason == "" {
			return nil
		}
		if row.Failures >= archiveAttempts {
			return r.holdArchive(ctx, row)
		}
		if row.Updated.Add(archiveRetry).After(r.now()) {
			return nil
		}
		return r.retryArchive(ctx, row)
	}
	r.launch(ctx, row)
	return nil
}

// holdArchive ends the retries: the box stops with its volume, the session stays closing, and a notice that wakes the parent
// says how to finish. A closing parent would wait on this session, so the session moves up to its nearest open ancestor.
// `session close` on a held session starts the archive again.
func (r *Reconciler) holdArchive(ctx context.Context, row store.LifecycleBox) error {
	session, err := r.store.Session(ctx, row.Session)
	if err != nil {
		return err
	}
	to, direct, err := r.store.OpenAncestor(ctx, row.Session)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("The archive of session %s failed %d times, so its box is stopped and the session stays closing. Its volume is kept. `dyna session close %s` tries the archive again. `dyna session close %s --discard`, which only the owner can run, deletes the volume and whatever was not pushed. Last failure: %s", session.Name, archiveAttempts, row.Session, row.Session, row.Reason)
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[:4096], "")
	}
	engine := contract.Principal{Kind: contract.PrincipalEngine}
	notice, err := r.event(session.Room, engine, contract.SessionNotice{Session: row.Session, Text: text})
	if err != nil {
		return err
	}
	var moved *contract.Event
	if !direct {
		event, err := r.event(session.Room, engine, contract.SessionMoved{Session: row.Session, NewParent: to})
		if err != nil {
			return err
		}
		moved = &event
	}
	if err := r.store.HoldArchive(ctx, row.Session, to, notice, moved); err != nil {
		return err
	}
	r.Kick()
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
	remove := row.Want == store.WantDiscard || !hadTurns
	if !remove {
		err = r.boxes.Start(ctx, row.ID)
		// A box with no container and no volume on its host has nothing to archive, and starting it fails every time.
		var nothing box.NothingOnHost
		remove = errors.As(err, &nothing)
	}
	if remove {
		if err = r.boxes.Remove(ctx, row.ID); err == nil {
			next := row.Box
			next.State, next.Reason = contract.BoxDeleted, ""
			_, err = r.change(ctx, row.Box, next, time.Time{}, row.Failures, "")
			return err
		}
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
	if row.Deleting && reason != "" {
		if reasonErr := r.store.SetRoomDeleteReason(ctx, row.Room, fmt.Sprintf("delete room %s: box %s: %s", row.Room, row.ID, reason)); reasonErr != nil {
			return reasonErr
		}
	}
	return err
}
