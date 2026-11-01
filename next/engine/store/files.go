package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// VisualPostSeq finds an accessible post by its event ID.
func (s *Store) VisualPostSeq(ctx context.Context, id contract.EventID, room contract.RoomID, owner bool) (int64, error) {
	var seq int64
	err := s.Read().QueryRowContext(ctx, "SELECT seq FROM events WHERE id=? AND (? OR room=?) AND type='post.created'", id, owner, room).Scan(&seq)
	return seq, err
}

// FileMedia finds the latest accessible media type for a file and the room that holds it. A session reads its room's files
// and any post delivered to it, which a mention can bring from another room.
// A room going away is passed over, so the owner reads a blob from a room that keeps it.
func (s *Store) FileMedia(ctx context.Context, hash string, room contract.RoomID, session contract.SessionID, owner bool) (string, contract.RoomID, bool, error) {
	var media string
	var home contract.RoomID
	err := s.Read().QueryRowContext(ctx, "SELECT f.media,e.room FROM event_files f JOIN events e ON e.seq=f.seq JOIN rooms r ON r.id=e.room WHERE f.sha256=? AND (? OR e.room=? OR EXISTS (SELECT 1 FROM deliveries d WHERE d.seq=f.seq AND d.to_kind=? AND d.to_id=?)) AND r.state='open' ORDER BY f.seq DESC LIMIT 1", hash, owner, room, contract.PrincipalSession, session).Scan(&media, &home)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return media, home, err == nil, err
}
