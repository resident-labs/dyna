package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// turnReplies reads the inputs and posts made in this turn's transaction window.
func turnReplies(ctx context.Context, tx *sql.Tx, session contract.SessionID, turn contract.TurnID) ([]contract.Delivery, map[room.Destination]contract.EventID, error) {
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
	rows, err = tx.QueryContext(ctx, "SELECT id,body FROM events WHERE from_kind=? AND from_id=? AND type='post.created' AND seq>(SELECT seq FROM events WHERE type='turn.started' AND json_extract(body,'$.turn')=?) ORDER BY seq", contract.PrincipalSession, session, turn)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	posts := make(map[room.Destination]contract.EventID)
	for rows.Next() {
		var id contract.EventID
		var raw []byte
		var post contract.PostCreated
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(raw, &post); err != nil {
			return nil, nil, err
		}
		for _, input := range inputs {
			channel := input.Reply
			if channel.Kind != contract.ReplyFinal {
				continue
			}
			matched := channel.Target.Kind == contract.PrincipalOwner && channel.Home == post.Home
			for _, mentioned := range post.Mentions {
				if mentioned == channel.Target {
					matched = true
				}
			}
			if matched {
				posts[room.Destination{Target: channel.Target, Home: channel.Home}] = id
			}
		}
	}
	return inputs, posts, rows.Err()
}
