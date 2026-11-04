package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ExpireReplayKeys removes keys older than the retry window.
func (s *Store) ExpireReplayKeys(ctx context.Context, before time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM client_replays WHERE created_at<?", before.UnixMilli())
		return err
	})
}

// RequestRemoval ends a close's wait for the box's diff: the reconciler removes the box next. A close that already moved on
// is left alone, because the box may report twice.
func (s *Store) RequestRemoval(ctx context.Context, session contract.SessionID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET want='discard',due_at=0 WHERE session=? AND state='closing' AND want='close'`, session)
		return err
	})
}

// SetCloseRetry records a retry of a removal only if the observed close intent still matches.
func (s *Store) SetCloseRetry(ctx context.Context, id contract.BoxID, want BoxWant, due time.Time, reason string, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT reason FROM boxes WHERE id=? AND state='closing' AND want=?", id, want).Scan(&previous)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE boxes SET due_at=?,reason=?,updated_at=? WHERE id=? AND state='closing' AND want=?", due.UnixMilli(), reason, event.Time.UnixMilli(), id, want)
		if err != nil {
			return err
		}
		if previous != reason {
			_, err = s.AppendEvent(tx, event)
		}
		return err
	})
}
