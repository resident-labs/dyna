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
	rows, err := s.Read().QueryContext(ctx, "SELECT id,session,parent,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures FROM boxes WHERE NOT ? OR NOT (state='deleted' AND EXISTS (SELECT 1 FROM sessions WHERE sessions.id=boxes.session AND sessions.closed_at IS NOT NULL)) ORDER BY created_at,id", activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boxes []LifecycleBox
	for rows.Next() {
		var b LifecycleBox
		var parent, host sql.NullString
		var size contract.BoxSizeName
		var created, updated, due int64
		if err := rows.Scan(&b.ID, &b.Session, &parent, &host, &b.Image, &size, &b.State, &b.Reason, &b.Slice, &created, &updated, &b.Want, &due, &b.Failures); err != nil {
			return nil, err
		}
		b.BudgetParent = contract.BoxID(parent.String)
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
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,updated_at=?,due_at=CASE WHEN ?='ready' THEN 0 ELSE due_at END,failures=CASE WHEN ?='ready' THEN 0 ELSE failures END WHERE id=? AND state=? AND (?!='ready' OR due_at=0 OR due_at>?)", host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, event.Time.UnixMilli(), next.State, next.State, next.ID, b.State, next.State, event.Time.UnixMilli())
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

// BoxAncestry returns budget ancestors and the box, parent first.
func (s *Store) BoxAncestry(ctx context.Context, id contract.BoxID) ([]box.Box, error) {
	rows, err := s.Read().QueryContext(ctx, `WITH RECURSIVE path(id,session,parent,state,depth) AS (
  SELECT id,session,parent,state,0 FROM boxes WHERE id=?
  UNION ALL SELECT b.id,b.session,b.parent,b.state,p.depth+1 FROM boxes b JOIN path p ON b.id=p.parent
 ) SELECT id,session,state FROM path ORDER BY depth DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []box.Box
	for rows.Next() {
		var b box.Box
		if err := rows.Scan(&b.ID, &b.Session, &b.State); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
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
		if old.State != contract.BoxPending {
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
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,updated_at=?,due_at=?,failures=? WHERE id=? AND state=? AND (NOT ? OR (want='run' AND due_at<=?))`, host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, event.Time.UnixMilli(), dueMS, failures, old.ID, old.State, admission, event.Time.UnixMilli())
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
func (s *Store) FinishCreate(ctx context.Context, b box.Box, image, failure string, due time.Time, event contract.Event) error {
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
		if failure != "" {
			to = contract.BoxFailed
		} else if state == contract.BoxCreating {
			to = contract.BoxStarting
		}
		if to != state {
			_, body, err := box.Transition(b, to, failure, event.Time)
			if err != nil {
				return err
			}
			body.Cause = event.Body.(contract.BoxChanged).Cause
			event.Body = body
		}
		dueMS := int64(0)
		if to == contract.BoxStarting && !due.IsZero() {
			dueMS = due.UnixMilli()
		}
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET image=?,state=?,reason=?,due_at=? WHERE id=? AND state=?`, image, to, failure, dueMS, b.ID, state)
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
