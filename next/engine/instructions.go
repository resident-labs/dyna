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

// SetInstructionsPublisher configures a nonblocking callback before serving requests.
// The callback marks the save synchronously and starts any remote work itself.
func (s *Service) SetInstructionsPublisher(publish func(context.Context)) {
	s.publishInstructions = publish
}

func (s *Service) instructionsPath(ctx context.Context, room contract.RoomID) (string, *contract.Error) {
	if room != "" {
		if err := s.store.RoomExists(ctx, room); err != nil {
			return "", failure(err)
		}
	}
	return s.layout.Instructions(room), nil
}

// writeInstructions replaces the file atomically.
func writeInstructions(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".instructions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(text)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
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
	save := func() error { return writeInstructions(path, req.Text) }
	if req.Room == "" {
		err = save()
	} else {
		err = s.roomWrite(ctx, req.Room, save)
	}
	if err != nil {
		return out, failure(err)
	}
	sum := sha256.Sum256([]byte(req.Text))
	out.Hash = hex.EncodeToString(sum[:])
	if s.publishInstructions != nil {
		s.publishInstructions(context.WithoutCancel(ctx))
	}
	return out, nil
}
