package engine

import (
	"context"
	"encoding/binary"
	"io"
	"sync"

	"github.com/resident-labs/dyna/contract"
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
// the session's computerLock, and writes the returned stop event after unlocking s.mu.
func (s *Service) clearComputerLocked(id contract.SessionID) (contract.RoomID, bool) {
	c := s.sessionLocked(id).computer
	if c == nil {
		return "", false
	}
	s.sessionLocked(id).computer = nil
	for ch := range c.watchers {
		close(ch)
		delete(c.watchers, ch)
	}
	return c.room, c.state == contract.ComputerRunning
}

func (s *Service) ComputerChanged(ctx context.Context, caller contract.Principal, req contract.ComputerChangedRequest) (contract.ComputerChangedResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ComputerChangedResponse{}, forbidden()
	}
	if req.To != contract.ComputerRunning && req.To != contract.ComputerStopped {
		return contract.ComputerChangedResponse{}, invalid("invalid computer state")
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	s.mu.Lock()
	state := s.sessionLocked(session.ID)
	s.mu.Unlock()
	state.computerLock.Lock()
	defer state.computerLock.Unlock()
	s.mu.Lock()
	if state.channel == nil {
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
	defer s.mu.Unlock()
	c.state = req.To
	if req.To == contract.ComputerStopped {
		c.lastFrame = nil
		wakeChannel(state.channel)
		for ch := range c.watchers {
			close(ch)
			delete(c.watchers, ch)
		}
	}
	return contract.ComputerChangedResponse{}, nil
}

func wakeChannel(channel *boxChannel) {
	select {
	case channel.wake <- struct{}{}:
	default:
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessionLocked(id)
	if box.State != contract.BoxReady || state.channel == nil {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	channel := state.channel
	c := state.computer
	if c == nil || c.state != contract.ComputerRunning {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	ch := make(chan []byte, 1)
	if len(c.watchers) == 0 {
		s.computerGeneration++
		c.generation = s.computerGeneration
		c.lastFrame = nil
		wakeChannel(channel)
	} else if c.lastFrame != nil {
		ch <- c.lastFrame
	}
	c.watchers[ch] = struct{}{}
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := c.watchers[ch]; !ok {
			return
		}
		delete(c.watchers, ch)
		close(ch)
		if len(c.watchers) == 0 && state.channel == channel {
			wakeChannel(channel)
		}
	}()
	return ch, nil
}

// computerInputHandle serializes enqueueing with box replacement.
type computerInputHandle struct {
	mu    sync.Mutex
	queue chan contract.ComputerInputBatch
	done  chan struct{}
}

// send and close serialize so an old queue cannot accept input after replacement.
func (h *computerInputHandle) send(ctx context.Context, batch contract.ComputerInputBatch) *contract.Error {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.done:
		return &contract.Error{Code: contract.ErrConflict, Message: "box replaced"}
	default:
	}
	select {
	case <-ctx.Done():
		return failure(ctx.Err())
	case h.queue <- batch:
		return nil
	default:
		return &contract.Error{Code: contract.ErrConflict, Message: "input queue is full"}
	}
}

func (h *computerInputHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.done:
	default:
		close(h.done)
	}
}

// ComputerInput queues owner events for the box's computer. It does not touch the session's turn.
func (s *Service) ComputerInput(ctx context.Context, caller contract.Principal, req contract.ComputerInputRequest) (contract.ComputerInputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.ComputerInputResponse{}, forbidden()
	}
	if _, err := s.store.Session(ctx, req.Session); err != nil {
		return contract.ComputerInputResponse{}, failure(err)
	}
	s.mu.Lock()
	state := s.sessionLocked(req.Session)
	if state.channel == nil {
		s.mu.Unlock()
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	if c := state.computer; c == nil || c.state != contract.ComputerRunning {
		s.mu.Unlock()
		return contract.ComputerInputResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	input := state.channel.input
	s.mu.Unlock()
	return contract.ComputerInputResponse{}, input.send(ctx, contract.ComputerInputBatch{Frame: req.Frame, Events: req.Events})
}

func (s *Service) ComputerFrames(ctx context.Context, caller contract.Principal, generation uint64, r io.Reader) *contract.Error {
	if caller.Kind != contract.PrincipalSession {
		return forbidden()
	}
	s.mu.Lock()
	state := s.sessionLocked(caller.Session)
	channel, computer := state.channel, state.computer
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if computer == nil || channel == nil || state.computer != computer || state.channel != channel || computer.generation != generation {
			return
		}
		computer.lastFrame = nil
		for ch := range computer.watchers {
			close(ch)
			delete(computer.watchers, ch)
		}
		channel.watching = 0
		wakeChannel(channel)
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
		s.mu.Lock()
		if c := state.computer; c != nil && c.state == contract.ComputerRunning && c.generation == generation {
			c.lastFrame = frame
			for ch := range c.watchers {
				select {
				case <-ch:
				default:
				}
				ch <- frame
			}
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
	}
}
