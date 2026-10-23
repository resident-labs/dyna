package subagent

import (
	"dyna-agent/internal/room"
)

// A session reads its own input and its wakes from its stored delivery rows
// (post_deliveries) instead of matching the post's address strings: a row names
// the session, so it keeps reaching that session after a rename, and its mode
// says whether the post starts a turn or waits for the next one. room.DeliveriesFor
// is the one write-time rule that stores the rows, so a reader here always
// agrees with the writer.

// sessionRows reads one session's delivery rows for postIDs. A test replaces it
// to see what a reader does when the post index cannot be read.
var sessionRows = func(rooms *room.Store, roomID string, keys, postIDs []string) (map[string]room.Delivery, error) {
	return rooms.SessionDeliveries(roomID, keys, postIDs)
}

// inputRows reads the delivery rows of one session for postIDs, keyed by post
// ID. A post without a row is not this session's.
//
// A read that fails is not an empty inbox: the caller must leave the room
// cursor and its cached pending posts where they are, so the same posts are
// read again instead of being skipped for good.
func inputRows(rooms *room.Store, roomID, sessionID, addr string, postIDs []string) (map[string]room.Delivery, error) {
	if rooms == nil || len(postIDs) == 0 {
		return nil, nil
	}
	return sessionRows(rooms, roomID, room.DeliveryKeys(sessionID, addr), postIDs)
}

// inputRow reads one post's delivery row for one session.
func inputRow(rooms *room.Store, roomID, sessionID, addr, postID string) (room.Delivery, bool, error) {
	rows, err := inputRows(rooms, roomID, sessionID, addr, []string{postID})
	if err != nil {
		return room.Delivery{}, false, err
	}
	row, ok := rows[postID]
	return row, ok, nil
}

// postIDs lists the post IDs of one log read, in log order.
func postIDs(msgs []room.Message) []string {
	ids := make([]string, len(msgs))
	for i, msg := range msgs {
		ids[i] = msg.ID
	}
	return ids
}

// wakes reports whether a stored delivery asks the session for a turn: a wake
// row does, and a quiet row waits for the next turn the session takes anyway. A
// post with no row for this session, such as one this session inherited from
// another's inbox, keeps its own wake field.
func wakes(rows map[string]room.Delivery, post room.Message) bool {
	if row, ok := rows[post.ID]; ok {
		return row.Mode == room.DeliveryWake
	}
	return post.Wake
}
