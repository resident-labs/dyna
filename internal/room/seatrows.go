package room

import (
	"errors"
	"slices"
	"strings"
)

// The root seat reads its deliveries from the stored rows (post_deliveries)
// instead of working them out again from the post's address strings at read
// time: a row for SeatSessionID says the post reached the seat, and its mode
// and category say what the post means for it. RootContext is the one
// classifier that writes those rows, so every reader agrees with the writer by
// construction.

// seatRowChunk bounds the post IDs of one row query, well below SQLite's
// variable limit.
const seatRowChunk = 500

// seatRowScanLimit bounds how many of the seat's newest rows one read looks at
// when it skips rows whose post the log does not hold.
const seatRowScanLimit = 20

// SeatDeliveries reads the root seat's delivery rows of roomID for postIDs,
// keyed by post ID. A post without a row is not the seat's: it neither wakes
// the seat nor adds context for it.
func (s *Store) SeatDeliveries(roomID string, postIDs []string) (map[string]Delivery, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	seat := SeatSessionID(roomID)
	out := make(map[string]Delivery, len(postIDs))
	for chunk := range slices.Chunk(postIDs, seatRowChunk) {
		query := "SELECT post_id, mode, category FROM post_deliveries WHERE session_id=? AND post_id IN (?" + strings.Repeat(",?", len(chunk)-1) + ")"
		args := make([]any, 0, len(chunk)+1)
		args = append(args, seat)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := Delivery{SessionID: seat}
			var mode string
			if err := rows.Scan(&d.PostID, &mode, &d.Category); err != nil {
				rows.Close()
				return nil, err
			}
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

// SeatDelivery reads the root seat's delivery row for one post.
func (s *Store) SeatDelivery(roomID, postID string) (Delivery, bool, error) {
	rows, err := s.SeatDeliveries(roomID, []string{postID})
	if err != nil {
		return Delivery{}, false, err
	}
	d, ok := rows[postID]
	return d, ok, nil
}

// NewestSeatDelivery reads the root seat's newest delivery whose post the room
// log holds, in log order: the post that last reached the seat, whatever it
// asked for. It reports false when no post ever reached the seat.
//
// A row can be stored just before its post's line, so a reader must not act on
// a row without its post: an append that failed removes its own rows, and a
// crash between the two steps leaves one behind. Such a row is skipped here.
func (s *Store) NewestSeatDelivery(roomID string) (Delivery, bool, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return Delivery{}, false, err
	}
	seat := SeatSessionID(roomID)
	// Posts are indexed in log order, and a home row carries their time, so ts
	// orders the seat's rows as the log does. Rows stored in the same
	// millisecond keep their insertion order.
	rows, err := db.Query(`SELECT d.post_id, d.mode, d.category FROM post_deliveries d
LEFT JOIN post_homes h ON h.room_id = d.room_id AND h.post_id = d.post_id
WHERE d.session_id=? ORDER BY h.ts DESC, d.rowid DESC LIMIT ?`, seat, seatRowScanLimit)
	if err != nil {
		return Delivery{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		d := Delivery{SessionID: seat}
		var mode string
		if err := rows.Scan(&d.PostID, &mode, &d.Category); err != nil {
			return Delivery{}, false, err
		}
		if _, err := s.Message(roomID, d.PostID); err != nil {
			if errors.Is(err, ErrPostNotFound) {
				continue
			}
			return Delivery{}, false, err
		}
		d.Mode = DeliveryMode(mode)
		return d, true, nil
	}
	return Delivery{}, false, rows.Err()
}
