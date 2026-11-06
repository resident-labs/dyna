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
		err := tx.QueryRowContext(ctx, `UPDATE boxes SET want='stop',due_at=0 WHERE session=? AND want='run' AND state IN ('pending','creating','starting','stopped') RETURNING id`, session).Scan(&id)
		if err == sql.ErrNoRows {
			// An identical Stop accepts the request without changing its durable retry state.
			err = tx.QueryRowContext(ctx, `SELECT id FROM boxes WHERE session=? AND want='stop' AND state IN ('pending','creating','starting','stopped')`, session).Scan(&id)
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE deliveries SET wake=0 WHERE to_kind='session' AND to_id=? AND state='pending'`, session)
		return err
	})
	return id, id != "", err
}

// RetryStartStop records failed cleanup without passing through run-only admission/start completion.
// A wake, close or newer retry schedule fences the stale stop result without delaying the new intent.
func (s *Store) RetryStartStop(ctx context.Context, row LifecycleBox, due time.Time, failures int) error {
	var oldDue int64
	if !row.DueAt.IsZero() {
		oldDue = row.DueAt.UnixMilli()
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE boxes SET due_at=?,failures=? WHERE id=? AND state=? AND want='stop' AND due_at=?`, due.UnixMilli(), failures, row.ID, row.State, oldDue)
		return err
	})
}
