package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// RequestTurnStop persists the request and wakes the box channel after commit.
func (s *Store) RequestTurnStop(ctx context.Context, session contract.SessionID, now time.Time) (bool, error) {
	var stopping bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE turns SET stop_requested_at=COALESCE(stop_requested_at,?) WHERE session=? AND ended_at IS NULL", now.UnixMilli(), session)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		stopping = n > 0
		if stopping {
			s.wakePending = append(s.wakePending, session)
		}
		return err
	})
	return stopping, err
}

func (s *Store) StopRequested(ctx context.Context, session contract.SessionID) (contract.TurnID, error) {
	var turn contract.TurnID
	err := s.Read().QueryRowContext(ctx, "SELECT id FROM turns WHERE session=? AND ended_at IS NULL AND stop_requested_at IS NOT NULL", session).Scan(&turn)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return turn, err
}

func (s *Store) PauseRoom(ctx context.Context, room contract.RoomID, paused bool, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var at *int64
		if paused {
			value := event.Time.UnixMilli()
			at = &value
		}
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET paused_at=? WHERE id=? AND (paused_at IS NOT NULL)!=?", at, room, paused)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM rooms WHERE id=?)", room).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return sql.ErrNoRows
			}
			return nil
		}
		if _, err := s.AppendEvent(tx, event); err != nil {
			return err
		}
		if paused {
			return nil
		}
		_, err = tx.ExecContext(ctx, `WITH RECURSIVE ancestors(id,parent) AS (
   SELECT b.id,b.parent FROM boxes b JOIN sessions s ON s.id=b.session
   WHERE s.room=? AND EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.state='pending' AND d.wake=1)
   UNION SELECT b.id,b.parent FROM boxes b JOIN ancestors a ON b.id=a.parent
  ) UPDATE boxes SET want='run' WHERE id IN (SELECT id FROM ancestors) AND want='stop'`, room)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM sessions WHERE room=? AND closed_at IS NULL", room)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id contract.SessionID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			s.wakePending = append(s.wakePending, id)
		}
		return rows.Err()
	})
}
