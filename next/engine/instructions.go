package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// SetInstructionsPublisher configures host replication before serving requests.
func (s *Service) SetInstructionsPublisher(publish func(context.Context)) {
	s.publishInstructions = publish
}

func (s *Service) instructionsPath(ctx context.Context, room contract.RoomID) (string, *contract.Error) {
	root := filepath.Join(s.filesDir, "instructions")
	if room == "" {
		return filepath.Join(root, "global", "AGENTS.md"), nil
	}
	id, err := s.store.RoomID(ctx, room)
	if err != nil {
		return "", failure(err)
	}
	return filepath.Join(root, "rooms", string(id), "AGENTS.md"), nil
}

func readInstructions(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	return string(data), hex.EncodeToString(sum[:]), nil
}

func (s *Service) InstructionsGet(ctx context.Context, caller contract.Principal, req contract.InstructionsGetRequest) (contract.InstructionsGetResponse, *contract.Error) {
	var out contract.InstructionsGetResponse
	if caller.Kind != contract.PrincipalOwner {
		return out, forbidden()
	}
	path, problem := s.instructionsPath(ctx, req.Room)
	if problem != nil {
		return out, problem
	}
	s.instructionsMu.Lock()
	defer s.instructionsMu.Unlock()
	text, hash, err := readInstructions(path)
	if err != nil {
		return out, failure(err)
	}
	out = contract.InstructionsGetResponse{Text: text, Hash: hash}
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, failure(err)
	}
	if err == nil {
		saved := info.ModTime().UTC()
		out.SavedAt = &saved
	}
	return out, nil
}

func (s *Service) InstructionsSet(ctx context.Context, caller contract.Principal, req contract.InstructionsSetRequest) (contract.InstructionsSetResponse, *contract.Error) {
	var out contract.InstructionsSetResponse
	if caller.Kind != contract.PrincipalOwner {
		return out, forbidden()
	}
	path, problem := s.instructionsPath(ctx, req.Room)
	if problem != nil {
		return out, problem
	}
	s.instructionsMu.Lock()
	defer s.instructionsMu.Unlock()
	_, hash, err := readInstructions(path)
	if err != nil {
		return out, failure(err)
	}
	if req.Base != hash {
		return out, &contract.Error{Code: contract.ErrStale, Message: "instructions changed since they were read"}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return out, failure(err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".instructions-*")
	if err != nil {
		return out, failure(err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(req.Text)
	closeErr := f.Close()
	if writeErr != nil {
		return out, failure(writeErr)
	}
	if closeErr != nil {
		return out, failure(closeErr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return out, failure(err)
	}
	sum := sha256.Sum256([]byte(req.Text))
	out.Hash = hex.EncodeToString(sum[:])
	if s.publishInstructions != nil {
		s.publishInstructions(ctx)
	}
	return out, nil
}
