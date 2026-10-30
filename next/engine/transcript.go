package engine

import (
	"context"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptAppend stores a box's transcript bytes at the acknowledged offset.
// Bytes below the offset are already stored, so only the part past it is written.
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
	end := req.Offset + int64(len(req.Bytes))
	if end <= acked {
		return contract.TranscriptAppendResponse{AckedOffset: acked}, nil
	}
	name := filepath.Join(s.transcriptDir, string(caller.Session), req.Path)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	defer file.Close()
	tail := req.Bytes[acked-req.Offset:]
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
