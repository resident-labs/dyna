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

// TranscriptTurnStart identifies a turn's first byte in a transcript file.
type TranscriptTurnStart struct {
	Turn  contract.TurnID
	Start int64
}

// CommitTranscriptOffset commits an acknowledged offset and discovered turn starts together.
func (s *Store) CommitTranscriptOffset(ctx context.Context, session contract.SessionID, path string, offset int64, starts []TranscriptTurnStart) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO transcript_offsets(session,path,acked) VALUES(?,?,?) ON CONFLICT(session,path) DO UPDATE SET acked=max(transcript_offsets.acked,excluded.acked)", session, path, offset); err != nil {
			return err
		}
		for _, start := range starts {
			if _, err := tx.ExecContext(ctx, "INSERT INTO transcript_turns(session,turn,path,start) VALUES(?,?,?,?) ON CONFLICT(session,turn,path) DO UPDATE SET start=min(transcript_turns.start,excluded.start)", session, start.Turn, path, start.Start); err != nil {
				return err
			}
		}
		return nil
	})
}

// ResetTranscriptOffset forgets the acknowledgment of a file the engine no longer holds, so the box sends it again.
// CommitTranscriptOffset only moves forward, so this is the one way back.
func (s *Store) ResetTranscriptOffset(ctx context.Context, session contract.SessionID, path string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE transcript_offsets SET acked=0 WHERE session=? AND path=?", session, path)
		return err
	})
}

// Transcripts lists files in their insertion order, oldest first.
func (s *Store) Transcripts(ctx context.Context, session contract.SessionID) ([]contract.TranscriptFile, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT path,acked FROM transcript_offsets WHERE session=? ORDER BY rowid", session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []contract.TranscriptFile
	for rows.Next() {
		var f contract.TranscriptFile
		if err := rows.Scan(&f.Path, &f.Bytes); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}
