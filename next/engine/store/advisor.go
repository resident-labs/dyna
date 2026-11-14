package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// RoomAdvisor returns the room's open advisor, or ErrNotFound.
func (s *Store) RoomAdvisor(ctx context.Context, room contract.RoomID) (Session, error) {
	var id contract.SessionID
	if err := s.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND advisor=1 AND closed_at IS NULL", room).Scan(&id); err != nil {
		return Session{}, err
	}
	return s.Session(ctx, id)
}

// DeliveryTurnEnded returns the turn.ended of the turn that took the delivery of an event to a session, or false while that turn runs.
// A failed or not started turn hands its inputs back to wait without waking, and that end answers them too.
func (s *Store) DeliveryTurnEnded(ctx context.Context, session contract.SessionID, seq int64) (contract.TurnEnded, bool, error) {
	var body []byte
	err := s.Read().QueryRowContext(ctx, `SELECT e.body FROM deliveries d JOIN events e ON e.type='turn.ended' AND e.from_kind='session' AND e.from_id=d.to_id AND e.seq>d.seq
AND (json_extract(CAST(e.body AS TEXT),'$.turn')=d.turn OR d.turn IS NULL AND d.wake=0 AND d.state='pending')
WHERE d.to_kind='session' AND d.to_id=? AND d.seq=? ORDER BY e.seq DESC LIMIT 1`, session, seq).Scan(&body)
	var ended contract.TurnEnded
	if errors.Is(err, ErrNotFound) {
		return ended, false, nil
	}
	if err != nil {
		return ended, false, err
	}
	return ended, true, json.Unmarshal(body, &ended)
}

// OweReply makes the delivery of an event to a session answer to its sender, as a parent's post to a child does, and reports whether
// the delivery was still open to it. One whose turn has ended answers nobody any more.
func (s *Store) OweReply(ctx context.Context, session contract.SessionID, seq int64, to contract.SessionID) (bool, error) {
	var owed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE deliveries SET reply_kind=?,reply_target_kind=?,reply_target_id=?,reply_home=?,reply_to='' WHERE to_kind='session' AND to_id=? AND seq=? AND reply_kind=? AND state IN ('pending','taken')",
			contract.ReplyFinal, contract.PrincipalSession, to, contract.ConversationFeed, session, seq, contract.ReplyNone)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		owed = n > 0
		return err
	})
	return owed, err
}
