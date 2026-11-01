package engine

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/pi"
)

// TranscriptImage returns the image a read returned. Pi records it in the session file and the engine keeps a copy of
// that file, so a preview stores nothing new and needs no box.
func (s *Service) TranscriptImage(ctx context.Context, caller contract.Principal, session contract.SessionID, turn contract.TurnID, item string) (string, []byte, *contract.Error) {
	missing := &contract.Error{Code: contract.ErrNotFound, Message: "image not found"}
	if caller.Kind != contract.PrincipalOwner {
		return "", nil, forbidden()
	}
	if session == "" || turn == "" || item == "" {
		return "", nil, missing
	}
	room, err := s.store.TranscriptRoom(ctx, session)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil, missing
	}
	if err != nil {
		return "", nil, failure(err)
	}
	files, err := s.store.TranscriptFiles(ctx, session)
	if err != nil {
		return "", nil, failure(err)
	}
	reader := pi.TranscriptReader{Dir: s.layout.Transcripts(room, session), Acked: make(map[string]int64, len(files))}
	for _, f := range files {
		reader.Acked[f.Path] = f.Acked
	}
	deliveries, err := s.store.TranscriptDeliveries(ctx, session)
	if err != nil {
		return "", nil, failure(err)
	}
	ranges, err := s.store.TranscriptTurnRanges(ctx, session, turn)
	if err != nil {
		return "", nil, failure(err)
	}
	for _, r := range ranges {
		items, _, _, err := reader.Read(r.Path, r.Start, r.End, deliveries, item, 0, 1)
		if err != nil {
			return "", nil, failure(err)
		}
		for _, found := range items {
			if found.Image == nil || found.ImageData == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(found.ImageData)
			if err != nil {
				return "", nil, missing
			}
			return found.Image.Media, data, nil
		}
	}
	return "", nil, missing
}
