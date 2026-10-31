package engine

import (
	"context"
	"database/sql"
	"encoding/binary"
	"io"

	"github.com/resident-labs/dyna/contract"
)

type computerState struct {
	generation uint64
	state      contract.ComputerState
	room       contract.RoomID
	watchers   map[chan []byte]struct{}
}

func (s *Service) computerEvent(ctx context.Context, session contract.SessionID, room contract.RoomID, state contract.ComputerState) error {
	e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.ComputerChanged{Session: session, To: state})
	if err != nil {
		return err
	}
	return s.store.Write(ctx, func(tx *sql.Tx) error { _, err := s.store.AppendEvent(tx, e); return err })
}

func (s *Service) clearComputerLocked(id contract.SessionID) {
	c := s.computers[id]
	if c == nil {
		return
	}
	delete(s.computers, id)
	for ch := range c.watchers {
		close(ch)
		delete(c.watchers, ch)
	}
	if c.state == contract.ComputerRunning {
		_ = s.computerEvent(context.Background(), id, c.room, contract.ComputerStopped)
	}
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
	defer s.mu.Unlock()
	if s.channels[session.ID] == nil {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box channel is not ready"}
	}
	c := s.computers[session.ID]
	if c == nil {
		c = &computerState{state: contract.ComputerStopped, room: session.Room, watchers: make(map[chan []byte]struct{})}
		s.computers[session.ID] = c
	}
	if c.state == req.To {
		return contract.ComputerChangedResponse{}, nil
	}
	if err := s.computerEvent(ctx, session.ID, session.Room, req.To); err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	c.state = req.To
	if req.To == contract.ComputerStopped {
		wakeComputer(s.channels[session.ID])
		for ch := range c.watchers {
			close(ch)
			delete(c.watchers, ch)
		}
	}
	return contract.ComputerChangedResponse{}, nil
}

func wakeComputer(channel *boxChannel) {
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
	channel := s.channels[id]
	if box.State != contract.BoxReady || channel == nil {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	c := s.computers[id]
	if c == nil || c.state != contract.ComputerRunning {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "computer is not running"}
	}
	ch := make(chan []byte, 1)
	if len(c.watchers) == 0 {
		s.computerGeneration++
		c.generation = s.computerGeneration
		wakeComputer(channel)
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
		if len(c.watchers) == 0 && s.channels[id] == channel {
			wakeComputer(channel)
		}
	}()
	return ch, nil
}

func (s *Service) ComputerFrames(ctx context.Context, caller contract.Principal, generation uint64, r io.Reader) *contract.Error {
	if caller.Kind != contract.PrincipalSession {
		return forbidden()
	}
	s.mu.Lock()
	channel, computer := s.channels[caller.Session], s.computers[caller.Session]
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if computer == nil || channel == nil || s.computers[caller.Session] != computer || s.channels[caller.Session] != channel || computer.generation != generation {
			return
		}
		for ch := range computer.watchers {
			close(ch)
			delete(computer.watchers, ch)
		}
		channel.watching = 0
		wakeComputer(channel)
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
		if c := s.computers[caller.Session]; c != nil && c.state == contract.ComputerRunning && c.generation == generation {
			for ch := range c.watchers {
				select {
				case ch <- frame:
				default:
				}
			}
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
	}
}
