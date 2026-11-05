package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// RequestStartStop persists stop before cancelling runtime work, fencing a concurrent hello or create result.
// Already stopped starts accept a repeated request. New deliveries can wake the session normally.
func (s *Store) RequestStartStop(ctx context.Context, session contract.SessionID, now time.Time) (contract.BoxID, bool, error) {
	var id contract.BoxID
	err := s.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE boxes SET want='stop',due_at=0 WHERE session=? AND want IN ('run','stop') AND state IN ('pending','creating','starting','stopped') RETURNING id`, session).Scan(&id)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE deliveries SET wake=0 WHERE to_kind='session' AND to_id=? AND state='pending'`, session)
		return err
	})
	return id, id != "", err
}
