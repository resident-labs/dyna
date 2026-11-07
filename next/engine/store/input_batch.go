package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// InputBatchWindow is measured from the preceding turn's end, not an arrival.
const InputBatchWindow = time.Minute

type batchReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func batchDeadline(ctx context.Context, db batchReader, session contract.SessionID) (time.Time, error) {
	var ended sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT MAX(ended_at) FROM turns WHERE session=?", session).Scan(&ended)
	if err != nil || !ended.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(ended.Int64).Add(InputBatchWindow), nil
}

// InputBatchDeadline derives the current idle wait from durable state. A stream
// owns one scheduled kick; reconnecting recomputes it, never restarts the window.
func (s *Store) InputBatchDeadline(ctx context.Context, session contract.SessionID) (time.Time, error) {
	var ended sql.NullInt64
	err := s.Read().QueryRowContext(ctx, `SELECT MAX(t.ended_at) FROM turns t WHERE t.session=?1
 AND NOT EXISTS (SELECT 1 FROM turns WHERE session=?1 AND ended_at IS NULL)
 AND EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=?1 AND state='pending' AND state IN ('pending','taken') AND wake=1)
 AND NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=?1 AND state='pending' AND state IN ('pending','taken') AND wake=1 AND immediate=1)`, session).Scan(&ended)
	if err != nil || !ended.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(ended.Int64).Add(InputBatchWindow), nil
}
