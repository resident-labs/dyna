package store

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func (s *Store) turnReplies(ctx context.Context, tx *sql.Tx, room contract.RoomID, session contract.SessionID, turn contract.TurnID) ([]contract.Delivery, []contract.Event, error) {
	rows, err := tx.QueryContext(ctx, "SELECT reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to FROM deliveries WHERE to_id=? AND turn=? ORDER BY seq", session, turn)
	if err != nil {
		return nil, nil, err
	}
	var inputs []contract.Delivery
	for rows.Next() {
		var d contract.Delivery
		var home string
		if err := rows.Scan(&d.Reply.Kind, &d.Reply.Target.Kind, &d.Reply.Target.Session, &home, &d.Reply.ReplyTo); err != nil {
			rows.Close()
			return nil, nil, err
		}
		kind, id, _ := strings.Cut(home, ":")
		d.Reply.Home = contract.Conversation{Kind: contract.ConversationKind(kind), Session: contract.SessionID(id)}
		inputs = append(inputs, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	var start int64
	if err := tx.QueryRowContext(ctx, "SELECT started_at FROM turns WHERE id=? AND session=?", turn, session).Scan(&start); err != nil {
		return nil, nil, err
	}
	// The single writer prevents a post from committing during this read.
	posts, err := s.Events(ctx, room, 0, math.MaxInt, []contract.EventType{"post.created"})
	if err != nil {
		return nil, nil, err
	}
	// A same-millisecond prior post is safe: the target already received it.
	kept := posts[:0]
	for _, post := range posts {
		if !post.Time.Before(time.UnixMilli(start)) {
			kept = append(kept, post)
		}
	}
	return inputs, kept, nil
}
