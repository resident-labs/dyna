package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Older active retries cleared their deadline at Dispatch. Their counter
// alone also describes owner work after exhaustion or a non-model failure;
// use the prior cause and attempt count since success to distinguish those.
const legacyModelRetryPending = `(s.model_retry_at=0 AND s.model_retries>0
 AND (SELECT json_extract(body,'$.cause') FROM events WHERE room=s.room AND type='turn.ended'
  AND json_extract(body,'$.session')=s.id AND json_extract(body,'$.outcome')!='stopped' ORDER BY seq DESC LIMIT 1)='model_failed'
 AND s.model_retries=(SELECT COUNT(*) FROM events e WHERE e.room=s.room AND e.type='turn.ended'
  AND json_extract(e.body,'$.session')=s.id AND json_extract(e.body,'$.cause')='model_failed'
  AND e.seq>COALESCE((SELECT MAX(c.seq) FROM events c WHERE c.room=s.room AND c.type='turn.ended'
   AND json_extract(c.body,'$.session')=s.id AND json_extract(c.body,'$.outcome')='completed'),0)))`

// model_retry_at is a positive deadline, -1 for a deferred failure with no
// scheduled wake, or 0 once settled. Only positive values are timestamps.
// WakeModelRetries returns due failed-turn deliveries to the normal wake path.
func (s *Store) WakeModelRetries(ctx context.Context, now time.Time) error {
	var due bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sessions s JOIN rooms r ON r.id=s.room WHERE s.state='open' AND s.closed_at IS NULL AND r.paused_at IS NULL AND s.model_retry_at>0 AND s.model_retry_at<=? AND NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL) AND NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND state='pending' AND wake=1))`, now.UnixMilli()).Scan(&due)
	if err != nil || !due {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id FROM sessions s JOIN rooms r ON r.id=s.room
   WHERE s.state='open' AND s.closed_at IS NULL AND r.paused_at IS NULL
   AND s.model_retry_at>0 AND s.model_retry_at<=?
   AND NOT EXISTS (SELECT 1 FROM turns WHERE session=s.id AND ended_at IS NULL)
   AND NOT EXISTS (SELECT 1 FROM deliveries WHERE to_kind='session' AND to_id=s.id AND state='pending' AND wake=1)`, now.UnixMilli())
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
			// Keep the deadline until Dispatch consumes the wake. The failure
			// remains retrying even if the box cannot start immediately.
			result, err := tx.ExecContext(ctx, "UPDATE deliveries SET wake=1,immediate=1 WHERE to_kind='session' AND to_id=? AND state='pending'", id)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=-1 WHERE id=?", id); err != nil {
					return err
				}
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
