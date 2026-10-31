package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

var ErrMoveCycle = errors.New("move would make a cycle")

// MoveSession checks idleness and commits the new tree and recreate markers together.
func (s *Store) MoveSession(ctx context.Context, target, parent contract.SessionID, budgetParent contract.BoxID, event contract.Event, members []box.Box, boxEvents []contract.Event) (contract.SessionID, bool, error) {
	var busy contract.SessionID
	var uncreated bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var cycle bool
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL
		) SELECT EXISTS(SELECT 1 FROM sub WHERE id=?)`, target, parent).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return ErrMoveCycle
		}
		err := tx.QueryRowContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION ALL SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL
		) SELECT s.id, (b.image='' OR b.host IS NULL) FROM sessions s JOIN sub ON s.id=sub.id JOIN boxes b ON b.session=s.id WHERE s.state='closing' OR b.state NOT IN ('ready','stopped','pending') OR b.image='' OR b.host IS NULL
		  OR EXISTS (SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL)
		  OR COALESCE((SELECT shell_jobs FROM turns t WHERE t.session=s.id ORDER BY t.started_at DESC,t.rowid DESC LIMIT 1),0)!=0
		  ORDER BY s.id LIMIT 1`, target).Scan(&busy, &uncreated)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if busy != "" {
			return nil
		}
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND closed_at IS NULL", parent).Scan(&state); err != nil {
			return err
		}
		if state == "closing" {
			return ErrParentClosing
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET parent=? WHERE id=?", parent, target); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE boxes SET parent=? WHERE session=?", nullableBox(budgetParent), target); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT ? UNION ALL SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL
		) UPDATE boxes SET want='stop' WHERE session IN (SELECT id FROM sub) AND state='ready'`, target); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return busy, uncreated, err
}

// SetRecreated records the new slice after the container was created.
func (s *Store) SetRecreated(ctx context.Context, id contract.BoxID, slice string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET slice=? WHERE id=?", slice, id)
		return err
	})
}
