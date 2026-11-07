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
		// Cancel pending non-owner work, including due retries queued for a box.
		// Owner posts retain their wake, whether pending or assigned as steers.
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET wake=0 WHERE to_kind='session' AND to_id=? AND state='pending' AND NOT EXISTS (SELECT 1 FROM events e WHERE e.seq=deliveries.seq AND e.from_kind='owner' AND e.type='post.created')", session); err != nil {
			return err
		}
		cancelled, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=-1 WHERE id=? AND model_retry_at>0", session)
		if err != nil {
			return err
		}
		n, err := cancelled.RowsAffected()
		if err != nil {
			return err
		}
		stopping = n > 0
		// Stop cancels existing non-owner work, not owner messages. Fence that
		// cancellation here: inputs arriving later must keep their wake even
		// while the old turn is still ending.
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET wake=0 WHERE to_kind='session' AND to_id=? AND state='taken' AND queue IS NOT NULL AND NOT EXISTS (SELECT 1 FROM events e WHERE e.seq=deliveries.seq AND e.from_kind='owner' AND e.type='post.created') AND turn IN (SELECT id FROM turns WHERE session=? AND ended_at IS NULL)", session, session); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE turns SET stop_requested_at=COALESCE(stop_requested_at,?) WHERE session=? AND ended_at IS NULL", now.UnixMilli(), session)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		stopping = stopping || n > 0
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

// RoomGitHubWrites reads a room's githubWrites setting as stored: its repos may hold `*`. A room with none set has no groups.
func (s *Store) RoomGitHubWrites(ctx context.Context, room contract.RoomID) (contract.GitHubWrites, error) {
	var stored string
	var writes contract.GitHubWrites
	if err := s.Read().QueryRowContext(ctx, "SELECT github_writes FROM rooms WHERE id=?", room).Scan(&stored); err != nil {
		return writes, err
	}
	return writes, json.Unmarshal([]byte(stored), &writes)
}

// SetRoomGitHubWrites replaces an open room's githubWrites setting with values the engine has checked. A setting with no groups
// is stored as off.
func (s *Store) SetRoomGitHubWrites(ctx context.Context, room contract.RoomID, writes contract.GitHubWrites) error {
	stored := []byte("{}")
	if writes.On() {
		var err error
		if stored, err = json.Marshal(writes); err != nil {
			return err
		}
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET github_writes=? WHERE id=? AND state='open'", string(stored), room)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return ErrRoomNotFound
		}
		return err
	})
}

// MarkTurnModelSent records the first engine handoff to its model proxy for the active turn.
// A later rejection by the child proxy still counts. It is not the provider send or first response.
func (s *Store) MarkTurnModelSent(ctx context.Context, session contract.SessionID, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE turns SET model_sent_at=? WHERE session=? AND ended_at IS NULL AND model_sent_at=0", at.UnixMilli(), session)
		return err
	})
}
