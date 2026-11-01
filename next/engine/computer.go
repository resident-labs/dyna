package engine

import (
	"context"
	"encoding/binary"
	"io"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) computerEvent(ctx context.Context, session contract.SessionID, room contract.RoomID, state contract.ComputerState) error {
	e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.ComputerChanged{Session: session, To: state})
	if err != nil {
		return err
	}
	return s.store.RecordEvent(ctx, e)
}

func (s *Service) ComputerChanged(ctx context.Context, caller contract.Principal, req contract.ComputerChangedRequest) (contract.ComputerChangedResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ComputerChangedResponse{}, forbidden()
	}
	if req.To != contract.ComputerRunning && req.To != contract.ComputerStopped {
		return contract.ComputerChangedResponse{}, invalid("invalid computer state")
	}
	session, open, err := s.store.OpenSession(ctx, caller.Session)
	if err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	if !open {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	handle, ok := s.lockSession(session.ID)
	if !ok {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	defer handle.Unlock()
	if handle.Channel() == nil {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box channel is not ready"}
	}
	if handle.ComputerState() == req.To {
		return contract.ComputerChangedResponse{}, nil
	}
	if err := s.computerEvent(ctx, session.ID, session.Room, req.To); err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	handle.SetComputer(session.Room, req.To)
	return contract.ComputerChangedResponse{}, nil
}

func (s *Service) ComputerWatch(ctx context.Context, id contract.SessionID) (<-chan []byte, *contract.Error) {
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return nil, failure(err)
	}
	box, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return nil, failure(err)
	}
	handle, ok := s.lookupSession(id)
	if !ok {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	defer handle.Unlock()
	if box.State != contract.BoxReady || handle.Channel() == nil {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	watch := handle.WatchComputer()
	if watch == nil {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	go func() {
		<-ctx.Done()
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		handle.RemoveComputerWatch(watch)
	}()
	return watch.Events(), nil
}

// ComputerInput queues owner events for the box's computer. It does not touch the session's turn.
func (s *Service) ComputerInput(ctx context.Context, caller contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.ComputerInputResponse{}, forbidden()
	}
	if _, err := s.store.Session(ctx, req.Session); err != nil {
		return contract.ComputerInputResponse{}, failure(err)
	}
	handle, ok := s.lookupSession(req.Session)
	if !ok {
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	defer handle.Unlock()
	channel := handle.Channel()
	if channel == nil {
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	if handle.ComputerState() != contract.ComputerRunning {
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	return contract.ComputerInputResponse{}, channel.SendInput(ctx, contract.ComputerInputBatch{Frame: req.Frame, Events: req.Events})
}

func (s *Service) ComputerFrames(ctx context.Context, caller contract.Principal, generation uint64, r io.Reader) *contract.Error {
	if caller.Kind != contract.PrincipalSession {
		return forbidden()
	}
	handle, ok := s.lookupSession(caller.Session)
	if !ok {
		return &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	stream := handle.ComputerStream()
	handle.Unlock()
	defer func() {
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		handle.EndComputerStream(stream, generation)
	}()
	for {
		var size uint32
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			return nil
		}
		if size == 0 || size > 8<<20 {
			return invalid("invalid frame size")
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(r, frame); err != nil {
			return nil
		}
		if !handle.Lock() {
			return nil
		}
		handle.ComputerFrame(generation, frame)
		handle.Unlock()
		if ctx.Err() != nil {
			return nil
		}
	}
}
