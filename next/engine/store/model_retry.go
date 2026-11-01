package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// WakeModelRetries returns due failed-turn deliveries to the normal wake path.
func (s *Store) WakeModelRetries(ctx context.Context, now time.Time) error {
	var due bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.state='open' AND s.closed_at IS NULL AND r.paused_at IS NULL AND s.model_retry_at>0 AND s.model_retry_at<=? AND NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL))`, now.UnixMilli()).Scan(&due)
	if err != nil || !due {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id FROM sessions s JOIN rooms r ON r.id=s.room
   WHERE s.state='open' AND s.closed_at IS NULL AND r.paused_at IS NULL
   AND s.model_retry_at>0 AND s.model_retry_at<=?
   AND NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL)`, now.UnixMilli())
		if err != nil {
			return err
		}
		var ids []contract.SessionID
		for rows.Next() {
			var id contract.SessionID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=0 WHERE id=?", id); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, "UPDATE deliveries SET wake=1 WHERE to_kind='session' AND to_id=? AND state='pending'", id)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				continue
			}
			_, err = tx.ExecContext(ctx, `UPDATE boxes SET want='run' WHERE session=? AND want='stop'`, id)
			if err != nil {
				return err
			}
			s.wakePending = append(s.wakePending, id)
		}
		return nil
	})
}
