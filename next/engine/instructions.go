package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
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

// instructionsEditor finds who the caller saves as: the owner, or a room's root, which may read and edit every room's
// file and the global one. Any other session is refused.
func (s *Service) instructionsEditor(ctx context.Context, caller contract.Principal) (contract.InstructionsSaver, *contract.Error) {
	switch caller.Kind {
	case contract.PrincipalOwner:
		return contract.InstructionsSaver{Kind: contract.PrincipalOwner}, nil
	case contract.PrincipalSession:
		name, room, err := s.store.RootSession(ctx, caller.Session)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return contract.InstructionsSaver{}, failure(err)
		}
		return contract.InstructionsSaver{Kind: contract.PrincipalSession, Session: caller.Session, Name: name, RoomName: room}, nil
	}
	return contract.InstructionsSaver{}, &contract.Error{Code: contract.ErrForbidden, Message: "only the owner or a room's root edits instructions"}
}

// savedRecord is who made the save that left the file with this hash.
type savedRecord struct {
	Hash string                     `json:"hash"`
	By   contract.InstructionsSaver `json:"by"`
}

// readSaver returns who saved the file, when the record matches its hash.
func readSaver(path, hash string) (*contract.InstructionsSaver, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record savedRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Hash != hash {
		return nil, nil
	}
	return &record.By, nil
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
	if _, problem := s.instructionsEditor(ctx, caller); problem != nil {
		return out, problem
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
	if out.SavedBy, err = readSaver(s.layout.InstructionsSaver(req.Room), hash); err != nil {
		return out, failure(err)
	}
	return out, nil
}

func (s *Service) InstructionsSet(ctx context.Context, caller contract.Principal, req contract.InstructionsSetRequest) (contract.InstructionsSetResponse, *contract.Error) {
	var out contract.InstructionsSetResponse
	saver, problem := s.instructionsEditor(ctx, caller)
	if problem != nil {
		return out, problem
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
	sum := sha256.Sum256([]byte(req.Text))
	out.Hash = hex.EncodeToString(sum[:])
	record, err := json.Marshal(savedRecord{Hash: out.Hash, By: saver})
	if err != nil {
		return out, failure(err)
	}
	// The record goes first: if the file then fails to save, the record names a hash the file lacks, so it is ignored.
	save := func() error {
		if err := writeInstructions(s.layout.InstructionsSaver(req.Room), string(record)); err != nil {
			return err
		}
		return writeInstructions(path, req.Text)
	}
	if req.Room == "" {
		err = save()
	} else {
		err = s.roomWrite(ctx, req.Room, save)
	}
	if err != nil {
		return contract.InstructionsSetResponse{}, failure(err)
	}
	if s.publishInstructions != nil {
		s.publishInstructions(context.WithoutCancel(ctx))
	}
	return out, nil
}
