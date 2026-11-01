package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// FileInRoom reports whether a file was uploaded or used in this room.
func (s *Store) FileInRoom(ctx context.Context, hash string, room contract.RoomID) (bool, error) {
	var found bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM uploads WHERE sha256=?1 AND room=?2) OR EXISTS(SELECT 1 FROM event_files f JOIN events e ON e.seq=f.seq WHERE f.sha256=?1 AND e.room=?2)", hash, room).Scan(&found)
	return found, err
}

// RecordUpload records an uploaded file's room.
func (s *Store) RecordUpload(ctx context.Context, hash string, room contract.RoomID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT OR IGNORE INTO uploads(sha256,room) VALUES(?,?)", hash, room)
		return err
	})
}

// VisualPostSeq finds an accessible post by its event ID.
func (s *Store) VisualPostSeq(ctx context.Context, id contract.EventID, room contract.RoomID, owner bool) (int64, error) {
	var seq int64
	err := s.Read().QueryRowContext(ctx, "SELECT seq FROM events WHERE id=? AND (? OR room=?) AND type='post.created'", id, owner, room).Scan(&seq)
	return seq, err
}

// FileMedia finds the latest accessible media type for a file.
func (s *Store) FileMedia(ctx context.Context, hash string, room contract.RoomID, owner bool) (string, bool, error) {
	var media string
	err := s.Read().QueryRowContext(ctx, "SELECT f.media FROM event_files f JOIN events e ON e.seq=f.seq WHERE f.sha256=? AND (? OR e.room=?) ORDER BY f.seq DESC LIMIT 1", hash, owner, room).Scan(&media)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return media, err == nil, err
}
