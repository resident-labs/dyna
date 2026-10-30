package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptAppend stores a box's transcript bytes at the acknowledged offset.
// A chunk that starts below the offset must match the stored bytes it overlaps.
func (s *Service) TranscriptAppend(ctx context.Context, caller contract.Principal, req contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TranscriptAppendResponse{}, forbidden()
	}
	if !filepath.IsLocal(req.Path) || req.Offset < 0 {
		return contract.TranscriptAppendResponse{}, invalid("transcript path must be local and offset must not be negative")
	}
	s.transcriptMu.Lock()
	defer s.transcriptMu.Unlock()
	acked, err := s.store.TranscriptOffset(ctx, caller.Session, req.Path)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if req.Offset > acked {
		return contract.TranscriptAppendResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "transcript chunk starts past the acknowledged offset"}
	}
	name := filepath.Join(s.transcriptDir, string(caller.Session), req.Path)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	defer file.Close()
	end := req.Offset + int64(len(req.Bytes))
	overlap := end
	if overlap > acked {
		overlap = acked
	}
	overlap -= req.Offset
	stored := make([]byte, overlap)
	if _, err := file.ReadAt(stored, req.Offset); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if !bytes.Equal(stored, req.Bytes[:overlap]) {
		return contract.TranscriptAppendResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "transcript chunk differs from stored bytes"}
	}
	tail := req.Bytes[overlap:]
	if len(tail) == 0 {
		return contract.TranscriptAppendResponse{AckedOffset: acked}, nil
	}
	if _, err := file.WriteAt(tail, acked); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if err := file.Sync(); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if err := s.store.SetTranscriptOffset(ctx, caller.Session, req.Path, end); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	return contract.TranscriptAppendResponse{AckedOffset: end}, nil
}
