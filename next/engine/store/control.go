package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
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

// SetRoomRootApproves turns on or off the kinds of operation the room's root may decide; a kind left out stays.
func (s *Store) SetRoomRootApproves(ctx context.Context, room contract.RoomID, changes map[contract.RootApproval]bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT root_approves FROM rooms WHERE id=? AND state='open'", room).Scan(&current); err != nil {
			return err
		}
		var before []contract.RootApproval
		if err := json.Unmarshal([]byte(current), &before); err != nil {
			return err
		}
		after := []contract.RootApproval{}
		for _, kind := range contract.AllRootApprovals {
			on, changed := changes[kind]
			if !changed {
				on = slices.Contains(before, kind)
			}
			if on {
				after = append(after, kind)
			}
		}
		set, err := json.Marshal(after)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE rooms SET root_approves=? WHERE id=?", string(set), room)
		return err
	})
}

// RoomRootApproves reads a room's name and whether its root may decide the operations of a kind.
func (s *Store) RoomRootApproves(ctx context.Context, room contract.RoomID, kind contract.RootApproval) (name string, on bool, err error) {
	err = s.Read().QueryRowContext(ctx, "SELECT name,EXISTS(SELECT 1 FROM json_each(root_approves) WHERE value=?) FROM rooms WHERE id=?", kind, room).Scan(&name, &on)
	return
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
		_, err = tx.ExecContext(ctx, `UPDATE boxes SET want='run' WHERE want='stop' AND EXISTS (SELECT 1 FROM sessions s WHERE s.id=boxes.session AND s.room=? AND EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.state='pending' AND d.wake=1))`, room)
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

// MarkTurnActive records that a running turn just used a tool. A stall check counts a turn as moving from its latest activity,
// and a write within a minute of the last one adds nothing, so a busy turn costs one write a minute.
func (s *Store) MarkTurnActive(ctx context.Context, session contract.SessionID, turn contract.TurnID, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE turns SET active_at=?1 WHERE id=?2 AND session=?3 AND ended_at IS NULL AND active_at<?1-60000", at.UnixMilli(), turn, session)
		return err
	})
}
