package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
)

// SetConversationRead advances a conversation's read sequence without moving it backward.
func (s *Store) SetConversationRead(ctx context.Context, room contract.RoomID, conversation string, requested int64) (int64, error) {
	var seq int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "INSERT INTO conversation_reads(room,conversation,seq) VALUES(?,?,?) ON CONFLICT(room,conversation) DO UPDATE SET seq=MAX(seq,excluded.seq) RETURNING seq", room, conversation, requested).Scan(&seq)
	})
	return seq, err
}
