package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

var ErrMoveCycle = errors.New("move would make a cycle")

// MoveSession commits the tree change and event in one transaction.
func (s *Store) MoveSession(ctx context.Context, target, parent contract.SessionID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var cycle bool
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE sub(id) AS (SELECT ? UNION SELECT s.id FROM sessions s JOIN sub ON s.parent=sub.id WHERE s.closed_at IS NULL) SELECT EXISTS(SELECT 1 FROM sub WHERE id=?)`, target, parent).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return ErrMoveCycle
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
		_, err := s.AppendEvent(tx, event)
		return err
	})
}

// SetRecreated records the new slice after the container was created.
func (s *Store) SetRecreated(ctx context.Context, id contract.BoxID, slice, image string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET slice=?, image=? WHERE id=?", slice, image, id)
		return err
	})
}
