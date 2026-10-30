package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptOffset returns the acknowledged offset of a transcript file, or 0 when none is stored.
func (s *Store) TranscriptOffset(ctx context.Context, session contract.SessionID, path string) (int64, error) {
	var offset int64
	err := s.Read().QueryRowContext(ctx, "SELECT acked FROM transcript_offsets WHERE session=? AND path=?", session, path).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return offset, err
}

// SetTranscriptOffset records the acknowledged offset of a transcript file.
func (s *Store) SetTranscriptOffset(ctx context.Context, session contract.SessionID, path string, offset int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO transcript_offsets(session,path,acked) VALUES(?,?,?) ON CONFLICT(session,path) DO UPDATE SET acked=excluded.acked", session, path, offset)
		return err
	})
}
