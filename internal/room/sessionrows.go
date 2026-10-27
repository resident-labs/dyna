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
		query := "SELECT " + deliveryColumns + " FROM post_deliveries WHERE post_id IN (?" +
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
			d, err := scanDelivery(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
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

// SessionDeliveryRows reads every delivery row of one session in roomID, in no
// particular order. It is the read that replaced the session_queues blob: the
// rows hold the claim, cancellation and dismissal state of every post the
// session was sent in this room.
func (s *Store) SessionDeliveryRows(roomID string, keys []string) ([]Delivery, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + deliveryColumns + " FROM post_deliveries WHERE room_id=? AND session_id IN (?" +
		strings.Repeat(",?", len(keys)-1) + ")"
	args := make([]any, 0, len(keys)+1)
	args = append(args, roomID)
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliverySignature is the newest write of one session's delivery rows that
// claims, cancels, dismisses or delivers a post, and 0 when it has none. A post
// stored for the session does not move it: the queue version it replaces moved
// only when a delivery's state changed, and the app reads it to see that. It
// cannot move backwards: a row keeps the turn that claimed it, and a cancelled,
// dismissed or failed row counts even when no turn claimed it.
func (s *Store) DeliverySignature(roomID string, keys []string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return 0, err
	}
	query := "SELECT COALESCE(MAX(updated_at), 0) FROM post_deliveries WHERE room_id=? AND session_id IN (?" +
		strings.Repeat(",?", len(keys)-1) + ") AND (claimed_turn > 0 OR state IN ('cancelled','dismissed','failed'))"
	args := make([]any, 0, len(keys)+1)
	args = append(args, roomID)
	for _, key := range keys {
		args = append(args, key)
	}
	var signature int64
	err = db.QueryRow(query, args...).Scan(&signature)
	return signature, err
}

// deliveryColumns is the column list every delivery read scans, in the order
// scanDelivery reads them.
const deliveryColumns = "post_id, session_id, mode, category, state, claimed_turn, how, delivered_at"

// rowScanner is the part of *sql.Rows and *sql.Row a delivery read needs.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDelivery reads one row of deliveryColumns.
func scanDelivery(row rowScanner) (Delivery, error) {
	var sessionID, mode, category, state, how string
	var d Delivery
	if err := row.Scan(&d.PostID, &sessionID, &mode, &category, &state, &d.ClaimedTurn, &how, &d.DeliveredAt); err != nil {
		return Delivery{}, err
	}
	d.SessionID, d.Mode, d.Category, d.State, d.How = sessionID, DeliveryMode(mode), category, DeliveryState(state), DeliveryHow(how)
	return d, nil
}

// PostDeliveries reads every recipient row of one post, in session-id order.
func (s *Store) PostDeliveries(roomID, postID string) ([]Delivery, error) {
	if roomID == "" || postID == "" {
		return nil, nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT "+deliveryColumns+" FROM post_deliveries WHERE room_id=? AND post_id=? ORDER BY session_id", roomID, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
