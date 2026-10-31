package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const quiet = `NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL)
AND (EXISTS (SELECT 1 FROM rooms WHERE id=s.room AND paused_at IS NOT NULL) OR NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND wake=1 AND state='pending'))
AND NOT EXISTS (SELECT 1 FROM boxes child JOIN boxes parent ON child.parent=parent.id WHERE parent.session=s.id AND child.state NOT IN ('deleted','failed'))
AND EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND shell_jobs=0 AND ended_at<=? AND id=(SELECT id FROM turns WHERE session=s.id ORDER BY started_at DESC, rowid DESC LIMIT 1))`

func (s *Store) CanDispatch(ctx context.Context, id contract.SessionID) (bool, error) {
	var ok bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL AND s.state='open' AND b.want='run' AND b.state='ready')`, id).Scan(&ok)
	return ok, err
}

// StopQuiet requests a stop only while the session still satisfies the quiet policy.
func (s *Store) StopQuiet(ctx context.Context, id contract.SessionID, cutoff time.Time) (bool, error) {
	var changed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE boxes SET want='stop' WHERE session=? AND want='run' AND state='ready' AND EXISTS (SELECT 1 FROM sessions s WHERE s.id=boxes.session AND s.closed_at IS NULL AND s.state!='closing' AND `+quiet+`)`, id, cutoff.UnixMilli())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		changed = n != 0
		return err
	})
	return changed, err
}

// WakeTerminal reverses a quiet stop when a terminal opens.
func (s *Store) WakeTerminal(ctx context.Context, id contract.BoxID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET want='run' WHERE id=? AND want='stop'`, id)
		return err
	})
}
