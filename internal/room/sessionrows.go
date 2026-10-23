package room

import (
	"slices"
	"strings"
)

// A session reads its input and its wakes from the stored delivery rows
// (post_deliveries) instead of working out again from the post's address
// strings who received it. DeliveriesFor is the one write-time rule that stores
// the rows, so every reader agrees with the writer by construction. A row names
// the session itself, so it keeps reaching that session after a rename.

// sessionRowChunk bounds the post IDs of one row query, well below SQLite's
// variable limit.
const sessionRowChunk = 500

// DeliveryKeys lists the session_id values that name one session's rows. The
// first is the session ID, which keeps matching after a rename. The second is
// the address the session answers to today, which is the key DeliveriesFor
// stores for a recipient it could not bind to a session (see DeliveriesFor):
// such a post reached the session that held that address, and it stops reaching
// the name's next holder, as the address text did. Either part may be empty.
func DeliveryKeys(id, addr string) []string {
	keys := make([]string, 0, 2)
	if id != "" {
		keys = append(keys, id)
	}
	if addr != "" && addr != id {
		keys = append(keys, addr)
	}
	return keys
}

// SessionDeliveries reads a session's delivery rows of roomID for postIDs,
// keyed by post ID. keys are the session_id values that name the session (see
// DeliveryKeys). A post without a row is not this session's: it is not its
// input, and it does not wake it.
func (s *Store) SessionDeliveries(roomID string, keys, postIDs []string) (map[string]Delivery, error) {
	if len(keys) == 0 || len(postIDs) == 0 {
		return nil, nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Delivery, len(postIDs))
	postArgs := strings.Repeat(",?", len(keys)-1)
	for chunk := range slices.Chunk(postIDs, sessionRowChunk) {
		query := "SELECT post_id, session_id, mode, category FROM post_deliveries WHERE post_id IN (?" +
			strings.Repeat(",?", len(chunk)-1) + ") AND session_id IN (?" + postArgs + ")"
		args := make([]any, 0, len(chunk)+len(keys))
		for _, id := range chunk {
			args = append(args, id)
		}
		for _, key := range keys {
			args = append(args, key)
		}
		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sessionID, mode string
			var d Delivery
			if err := rows.Scan(&d.PostID, &sessionID, &mode, &d.Category); err != nil {
				rows.Close()
				return nil, err
			}
			d.SessionID = sessionID
			d.Mode = DeliveryMode(mode)
			out[d.PostID] = d
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
