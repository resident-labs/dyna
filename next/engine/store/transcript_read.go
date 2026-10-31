package store

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

type TranscriptFile struct {
	Path  string
	Acked int64
}

func (s *Store) TranscriptFiles(ctx context.Context, session contract.SessionID) ([]TranscriptFile, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT path,acked FROM transcript_offsets WHERE session=? ORDER BY path", session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []TranscriptFile
	for rows.Next() {
		var f TranscriptFile
		if err := rows.Scan(&f.Path, &f.Acked); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *Store) TranscriptDeliveries(ctx context.Context, session contract.SessionID) (map[contract.DeliverySeq]contract.TurnID, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT seq,turn FROM deliveries WHERE to_kind=? AND to_id=? AND turn IS NOT NULL", contract.PrincipalSession, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[contract.DeliverySeq]contract.TurnID)
	for rows.Next() {
		var seq contract.DeliverySeq
		var turn contract.TurnID
		if err := rows.Scan(&seq, &turn); err != nil {
			return nil, err
		}
		result[seq] = turn
	}
	return result, rows.Err()
}
