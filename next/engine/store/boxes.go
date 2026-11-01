package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// BoxWant is the durable desired lifecycle state of a box.
type BoxWant string

const (
	WantRun     BoxWant = "run"
	WantStop    BoxWant = "stop"
	WantClose   BoxWant = "close"
	WantDiscard BoxWant = "discard"
)

// LifecycleBox combines observed and desired state for reconciliation.
type LifecycleBox struct {
	box.Box
	Want     BoxWant
	DueAt    time.Time
	Failures int
	Paused   bool
	Deleting bool
	Limits   contract.RoomLimits
}

func (s *Store) Boxes(ctx context.Context) ([]box.Box, error) {
	rows, err := s.lifecycleBoxes(ctx, false)
	if err != nil {
		return nil, err
	}
	var boxes []box.Box
	for _, row := range rows {
		boxes = append(boxes, row.Box)
	}
	return boxes, nil
}

func (s *Store) LifecycleBoxes(ctx context.Context) ([]LifecycleBox, error) {
	return s.lifecycleBoxes(ctx, true)
}

func (s *Store) lifecycleBoxes(ctx context.Context, activeOnly bool) ([]LifecycleBox, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,session,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures,(SELECT room FROM sessions WHERE id=boxes.session),(SELECT r.cpus FROM rooms r JOIN sessions s ON s.room=r.id WHERE s.id=boxes.session),(SELECT r.mem_bytes FROM rooms r JOIN sessions s ON s.room=r.id WHERE s.id=boxes.session),EXISTS(SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.paused_at IS NOT NULL),EXISTS(SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.id=boxes.session AND r.state='deleting') FROM boxes WHERE NOT ? OR NOT (state='deleted' AND EXISTS (SELECT 1 FROM sessions WHERE sessions.id=boxes.session AND sessions.closed_at IS NOT NULL)) ORDER BY (SELECT created_at FROM sessions WHERE id=boxes.session),id", activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boxes []LifecycleBox
	for rows.Next() {
		var b LifecycleBox
		var host sql.NullString
		var size contract.BoxSizeName
		var created, updated, due int64
		if err := rows.Scan(&b.ID, &b.Session, &host, &b.Image, &size, &b.State, &b.Reason, &b.Slice, &created, &updated, &b.Want, &due, &b.Failures, &b.Room, &b.Limits.CPUs, &b.Limits.MemBytes, &b.Paused, &b.Deleting); err != nil {
			return nil, err
		}
		b.Host = contract.HostName(host.String)
		var ok bool
		b.Size, ok = box.Size(size)
		if !ok {
			return nil, box.TransitionError{From: b.State, To: b.State}
		}
		if due != 0 {
			b.DueAt = time.UnixMilli(due)
		}
		b.PlacedAt = time.UnixMilli(created)
		b.Updated = time.UnixMilli(updated)
		boxes = append(boxes, b)
	}
	return boxes, rows.Err()
}

func (s *Store) Box(ctx context.Context, id contract.BoxID) (box.Box, error) {
	boxes, err := s.Boxes(ctx)
	if err != nil {
		return box.Box{}, err
	}
	for _, b := range boxes {
		if b.ID == id {
			return b, nil
		}
	}
	return box.Box{}, sql.ErrNoRows
}

// TransitionBox changes the record and publishes its event in one transaction.
func (s *Store) TransitionBox(ctx context.Context, b box.Box, to contract.BoxState, reason string, event contract.Event) (box.Box, error) {
	next, body, err := box.Transition(b, to, reason, event.Time)
	if err != nil {
		return box.Box{}, err
	}
	body.Cause = event.Body.(contract.BoxChanged).Cause
	event.Body = body
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var host *contract.HostName
		if next.Host != "" {
			host = &next.Host
		}
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,updated_at=?,due_at=CASE WHEN ?='ready' THEN 0 ELSE due_at END WHERE id=? AND state=? AND (?!='ready' OR ? NOT IN ('creating','starting') OR due_at=0 OR due_at>?) AND (?!='lost' OR ?!='ready' OR EXISTS (SELECT 1 FROM hosts WHERE name=boxes.host AND state!='unreachable' AND state!='removed'))", host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, event.Time.UnixMilli(), next.State, next.ID, b.State, next.State, b.State, event.Time.UnixMilli(), b.State, next.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return next, err
}

// WakeLostInventory schedules inventory; an in-flight check may defer it by up to 60 seconds.
func (s *Store) WakeLostInventory(ctx context.Context, id contract.BoxID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET due_at=0 WHERE id=? AND state='lost' AND due_at!=0`, id)
		return err
	})
}

// SetBoxLifecycle commits an observed transition and retry data together.
// A stale observed state returns false without publishing an event.
func (s *Store) SetBoxLifecycle(ctx context.Context, old, next box.Box, due time.Time, failures int, event contract.Event) (bool, error) {
	changed := old.State != next.State || old.Reason != next.Reason
	if old.State != next.State {
		_, body, err := box.Transition(old, next.State, next.Reason, event.Time)
		if err != nil {
			return false, err
		}
		body.Host = next.Host
		body.Cause = event.Body.(contract.BoxChanged).Cause
		event.Body = body
	} else if changed {
		if old.State != contract.BoxPending && old.State != contract.BoxStarting {
			return false, box.TransitionError{From: old.State, To: next.State}
		}
		cause := event.Body.(contract.BoxChanged).Cause
		event.Body = contract.BoxChanged{Box: old.ID, Session: old.Session, Host: next.Host, From: old.State, To: next.State, Reason: next.Reason, Cause: cause}
	}
	var dueMS int64
	if !due.IsZero() {
		dueMS = due.UnixMilli()
	}
	var updated bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var host *contract.HostName
		if next.Host != "" {
			host = &next.Host
		}
		admission := (old.State == contract.BoxPending || old.State == contract.BoxStopped) && next.State != contract.BoxClosing
		// created_at is the allocation time for disk accounting, not the waiter's enqueue time.
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,created_at=CASE WHEN state='pending' AND ?='creating' AND image='' THEN ? ELSE created_at END,updated_at=?,due_at=?,failures=? WHERE id=? AND state=? AND (NOT ? OR (want='run' AND due_at<=?)) AND (?!='lost' OR ?='closing' OR EXISTS (SELECT 1 FROM hosts WHERE name=boxes.host AND state='ready')) AND (?!='lost' OR ?!='ready' OR want IN ('run','stop'))`, host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, next.State, event.Time.UnixMilli(), event.Time.UnixMilli(), dueMS, failures, old.ID, old.State, admission, event.Time.UnixMilli(), old.State, next.State, old.State, next.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		updated = true
		if changed {
			_, err = s.AppendEvent(tx, event)
		}
		return err
	})
	return updated, err
}

// FinishCreate records the host result without overwriting a concurrent hello.
func (s *Store) FinishCreate(ctx context.Context, b box.Box, image, failure string, due time.Time, failures int, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var state contract.BoxState
		if err := tx.QueryRowContext(ctx, `SELECT state FROM boxes WHERE id=?`, b.ID).Scan(&state); err != nil {
			return err
		}
		if state != contract.BoxCreating && state != contract.BoxStarting && state != contract.BoxReady {
			return nil
		}
		b.State = state
		to := state
		reason := ""
		if failure != "" {
			image = b.Image
			if state != contract.BoxCreating || failures >= 3 {
				to, reason = contract.BoxFailed, string(contract.CauseCreateFailed)
			} else {
				to, reason = contract.BoxPending, string(box.PendingCreateRetry)
			}
		} else if state == contract.BoxCreating {
			to = contract.BoxStarting
		}
		if to != state {
			_, body, err := box.Transition(b, to, reason, event.Time)
			if err != nil {
				return err
			}
			body.Cause = event.Body.(contract.BoxChanged).Cause
			if to == contract.BoxFailed {
				body.Cause = contract.CauseCreateFailed
			}
			event.Body = body
		}
		dueMS := int64(0)
		if (to == contract.BoxStarting || to == contract.BoxPending) && !due.IsZero() {
			dueMS = due.UnixMilli()
		}
		// A completed turn after an early hello may have reset the current failure count.
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET image=?,state=?,reason=?,due_at=?,created_at=CASE WHEN ?='' THEN ? ELSE created_at END,failures=CASE WHEN ?='ready' THEN failures ELSE ? END WHERE id=? AND state=?`, image, to, reason, dueMS, failure, event.Time.UnixMilli(), state, failures, b.ID, state)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if to != state {
			_, err = s.AppendEvent(tx, event)
		}
		return err
	})
}
