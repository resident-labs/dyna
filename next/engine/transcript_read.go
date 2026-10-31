package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

// TranscriptRead reads the acknowledged prefix without holding the writer or service lock.
func (s *Service) TranscriptRead(ctx context.Context, caller contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	response := contract.TranscriptReadResponse{Steps: []contract.TranscriptStep{}}
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	if req.Session == "" {
		return response, invalid("session is required")
	}
	if _, err := s.store.Session(ctx, req.Session); err != nil {
		return response, failure(err)
	}
	files, err := s.store.TranscriptFiles(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	deliveries, err := s.store.TranscriptDeliveries(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	for _, f := range files {
		file, err := os.Open(filepath.Join(s.transcriptDir, string(req.Session), f.Path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return response, failure(err)
		}
		steps, truncated, err := pi.ReadSteps(io.LimitReader(file, f.Acked), deliveries, req.Turn, 200)
		_ = file.Close()
		if err != nil {
			return response, failure(err)
		}
		response.Truncated = response.Truncated || truncated
		for i := range steps {
			steps[i].Label = shell.Mask(steps[i].Label)
		}
		response.Steps = append(response.Steps, steps...)
		sort.SliceStable(response.Steps, func(i, j int) bool { return response.Steps[i].Started.Before(response.Steps[j].Started) })
		if len(response.Steps) > 200 {
			response.Steps = response.Steps[len(response.Steps)-200:]
			response.Truncated = true
		}
	}
	sort.SliceStable(response.Steps, func(i, j int) bool { return response.Steps[i].Started.Before(response.Steps[j].Started) })
	if len(response.Steps) > 200 {
		response.Steps = response.Steps[len(response.Steps)-200:]
		response.Truncated = true
	}
	for {
		data, err := json.Marshal(response)
		if err != nil {
			return response, failure(err)
		}
		if len(data) <= 256<<10 || len(response.Steps) == 0 {
			break
		}
		response.Steps = response.Steps[1:]
		response.Truncated = true
	}
	return response, nil
}
