package room

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// SenderMuteKind identifies the kind of sender a mute matches.
type SenderMuteKind string

const (
	SenderMuteSession SenderMuteKind = "session"
	SenderMuteRoom    SenderMuteKind = "room"
)

// SetSenderMute sets an expiring mute, or removes it when until is zero.
// id is the sender's stable identity.
func (s *Store) SetSenderMute(roomID string, kind SenderMuteKind, id string, until time.Time) error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		if until.IsZero() {
			_, err := conn.ExecContext(context.Background(), "DELETE FROM room_sender_mutes WHERE room_id=? AND kind=? AND sender_id=?", roomID, kind, id)
			return err
		}
		_, err := conn.ExecContext(context.Background(), `INSERT INTO room_sender_mutes(room_id, kind, sender_id, until_at) VALUES(?,?,?,?)
 ON CONFLICT(room_id,kind,sender_id) DO UPDATE SET until_at=excluded.until_at`, roomID, kind, id, until.UnixMilli())
		return err
	})
}

func migrateSenderPolicy(db *sql.DB) error {
	return database.Migrate(db, "room.sender_policy", 1, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), `CREATE TABLE room_sender_mutes (
    room_id TEXT NOT NULL, kind TEXT NOT NULL, sender_id TEXT NOT NULL, until_at INTEGER NOT NULL,
    PRIMARY KEY(room_id,kind,sender_id)
   )`)
			return err
		}
		return nil
	})
}

// quietCrossRoomSender applies a fixed limit of three wakes in two minutes.
// It runs in the same transaction as the delivery row, so concurrent posts
// cannot both pass the limit.
func quietCrossRoomSender(ctx context.Context, conn *sql.Conn, msg Message) (bool, error) {
	if msg.Sender() != SenderSession || !senderFromAnotherRoom(msg) {
		return false, nil
	}
	id := msg.AuthorSessionID()
	var muted bool
	err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM room_sender_mutes
 WHERE room_id=? AND until_at>? AND ((kind='session' AND sender_id=?) OR (kind='room' AND sender_id=?)))`,
		msg.RoomID, msg.TS, id, msg.SenderRoomID).Scan(&muted)
	if err != nil || muted {
		return muted, err
	}
	var count int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_homes h JOIN post_deliveries d
 ON d.room_id=h.room_id AND d.post_id=h.post_id
 WHERE h.room_id=? AND h.author_session_id=? AND h.ts>? AND h.ts<=?
 AND d.session_id=? AND d.mode='wake'`, msg.RoomID, id, msg.TS-int64(2*time.Minute/time.Millisecond), msg.TS, SeatSessionID(msg.RoomID)).Scan(&count)
	return count >= 3, err
}
