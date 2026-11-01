package store

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (s *Store) turnReplies(ctx context.Context, tx *sql.Tx, room contract.RoomID, session contract.SessionID, turn contract.TurnID) ([]contract.Delivery, []contract.Event, error) {
	rows, err := tx.QueryContext(ctx, "SELECT reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to FROM deliveries WHERE to_kind=? AND to_id=? AND turn=? ORDER BY seq", contract.PrincipalSession, session, turn)
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
	if err := tx.QueryRowContext(ctx, "SELECT started_seq FROM turns WHERE id=? AND session=?", turn, session).Scan(&start); err != nil {
		return nil, nil, err
	}
	// The single writer prevents a post from committing during this read.
	posts, err := s.Events(ctx, room, start, math.MaxInt, []contract.EventType{"post.created"})
	return inputs, posts, err
}

// endPosts keeps the reported end posts of a completed turn that the session really sent. A repeated
// client ID can answer with a post from before this turn, so the check is by author, not by turn.
func (s *Store) endPosts(ctx context.Context, tx *sql.Tx, room contract.RoomID, session contract.SessionID, output contract.TurnOutputRequest) ([]contract.Event, error) {
	if output.Outcome != contract.TurnCompleted {
		return nil, nil
	}
	var posts []contract.Event
	for _, id := range output.EndPosts {
		rows, err := tx.QueryContext(ctx, "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE id=? AND room=? AND type='post.created' AND from_kind=? AND from_id=?", id, room, contract.PrincipalSession, session)
		if err != nil {
			return nil, err
		}
		if rows.Next() {
			post, err := scanEvent(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			posts = append(posts, post)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return posts, nil
}

// deliverEndPost gives a session target the post that answers it. The turn has no final text, so the
// post itself is the reply: a target it mentioned already has it, and any other target wakes on it.
func (s *Store) deliverEndPost(ctx context.Context, tx *sql.Tx, ended []contract.Event, posted contract.EventID, d contract.Delivery) error {
	i := slices.IndexFunc(ended, func(post contract.Event) bool { return post.ID == posted })
	post := ended[i]
	var delivered bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM deliveries WHERE to_kind=? AND to_id=? AND seq=?)", d.To.Kind, d.To.Session, post.Seq).Scan(&delivered)
	if err != nil || delivered {
		return err
	}
	d.Wake = true
	return s.InsertDelivery(ctx, tx, post.Seq, post.Body, d)
}
