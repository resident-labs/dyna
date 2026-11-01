package engine

import (
	"context"
	"encoding/binary"
	"io"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
)

type computerState struct {
	generation uint64
	state      contract.ComputerState
	room       contract.RoomID
	watchers   map[chan []byte]struct{}
	lastFrame  []byte
}

func (s *Service) computerEvent(ctx context.Context, session contract.SessionID, room contract.RoomID, state contract.ComputerState) error {
	e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.ComputerChanged{Session: session, To: state})
	if err != nil {
		return err
	}
	return s.store.RecordEvent(ctx, e)
}

// clearComputerLocked removes the current computer. The caller holds s.mu and
// the live session lock, and writes the returned stop event after unlocking s.mu.
func (s *Service) clearComputerLocked(id contract.SessionID) (contract.RoomID, bool, []chan []byte) {
	state := s.sessions[id]
	if state == nil || state.computer == nil {
		return "", false, nil
	}
	c := state.computer
	state.computer = nil
	var watchers []chan []byte
	for ch := range c.watchers {
		watchers = append(watchers, ch)
		delete(c.watchers, ch)
	}
	return c.room, c.state == contract.ComputerRunning, watchers
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
	handle, state, ok := s.lockSession(session.ID)
	if !ok {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	defer handle.Unlock()
	s.mu.Lock()
	if handle.Channel() == nil {
		s.mu.Unlock()
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box channel is not ready"}
	}
	c := state.computer
	if c == nil {
		c = &computerState{state: contract.ComputerStopped, room: session.Room, watchers: make(map[chan []byte]struct{})}
		state.computer = c
	}
	if c.state == req.To {
		s.mu.Unlock()
		return contract.ComputerChangedResponse{}, nil
	}
	s.mu.Unlock()
	if err := s.computerEvent(ctx, session.ID, session.Room, req.To); err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	s.mu.Lock()
	c.state = req.To
	var watchers []chan []byte
	var channel *live.Channel
	if req.To == contract.ComputerStopped {
		c.lastFrame = nil
		channel = handle.Channel()
		for ch := range c.watchers {
			watchers = append(watchers, ch)
			delete(c.watchers, ch)
		}
	}
	s.mu.Unlock()
	if channel != nil {
		channel.Wake()
	}
	for _, ch := range watchers {
		close(ch)
	}
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
	handle, state, ok := s.lookupSession(id)
	if !ok || state == nil {
		if ok {
			handle.Unlock()
		}
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	defer handle.Unlock()
	s.mu.Lock()
	if box.State != contract.BoxReady || handle.Channel() == nil {
		s.mu.Unlock()
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	channel := handle.Channel()
	c := state.computer
	if c == nil || c.state != contract.ComputerRunning {
		s.mu.Unlock()
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	ch := make(chan []byte, 1)
	first := len(c.watchers) == 0
	var frame []byte
	if first {
		s.computerGeneration++
		c.generation = s.computerGeneration
		c.lastFrame = nil
	} else {
		frame = c.lastFrame
	}
	c.watchers[ch] = struct{}{}
	s.mu.Unlock()
	if first {
		channel.Wake()
	} else if frame != nil {
		ch <- frame
	}
	go func() {
		<-ctx.Done()
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		s.mu.Lock()
		if _, ok := c.watchers[ch]; !ok {
			s.mu.Unlock()
			return
		}
		delete(c.watchers, ch)
		wake := len(c.watchers) == 0 && handle.Channel() == channel
		s.mu.Unlock()
		close(ch)
		if wake {
			channel.Wake()
		}
	}()
	return ch, nil
}

// ComputerInput queues owner events for the box's computer. It does not touch the session's turn.
func (s *Service) ComputerInput(ctx context.Context, caller contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.ComputerInputResponse{}, forbidden()
	}
	if _, err := s.store.Session(ctx, req.Session); err != nil {
		return contract.ComputerInputResponse{}, failure(err)
	}
	handle, state, ok := s.lookupSession(req.Session)
	if !ok || state == nil {
		if ok {
			handle.Unlock()
		}
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	defer handle.Unlock()
	s.mu.Lock()
	channel := handle.Channel()
	if channel == nil {
		s.mu.Unlock()
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	if c := state.computer; c == nil || c.state != contract.ComputerRunning {
		s.mu.Unlock()
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	s.mu.Unlock()
	return contract.ComputerInputResponse{}, channel.SendInput(ctx, contract.ComputerInputBatch{Frame: req.Frame, Events: req.Events})
}

func (s *Service) ComputerFrames(ctx context.Context, caller contract.Principal, generation uint64, r io.Reader) *contract.Error {
	if caller.Kind != contract.PrincipalSession {
		return forbidden()
	}
	handle, state, ok := s.lookupSession(caller.Session)
	if !ok || state == nil {
		if ok {
			handle.Unlock()
		}
		return &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	s.mu.Lock()
	channel, computer := handle.Channel(), state.computer
	s.mu.Unlock()
	handle.Unlock()
	defer func() {
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		s.mu.Lock()
		if computer == nil || channel == nil || state.computer != computer || handle.Channel() != channel || computer.generation != generation {
			s.mu.Unlock()
			return
		}
		computer.lastFrame = nil
		var watchers []chan []byte
		for ch := range computer.watchers {
			watchers = append(watchers, ch)
			delete(computer.watchers, ch)
		}
		channel.ResetComputerWatch()
		s.mu.Unlock()
		for _, ch := range watchers {
			close(ch)
		}
		channel.Wake()
	}()
	var watchers []chan []byte
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
		s.mu.Lock()
		watchers = watchers[:0]
		if c := state.computer; c != nil && c.state == contract.ComputerRunning && c.generation == generation {
			c.lastFrame = frame
			for ch := range c.watchers {
				watchers = append(watchers, ch)
			}
		}
		s.mu.Unlock()
		for _, ch := range watchers {
			select {
			case <-ch:
			default:
			}
			ch <- frame
		}
		handle.Unlock()
		if ctx.Err() != nil {
			return nil
		}
	}
}
