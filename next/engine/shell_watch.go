package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

type shellWatch struct {
	generation uint64
	watchers   map[chan contract.ShellJobOutput]struct{}
}

func (s *Service) clearShellLocked(id contract.SessionID) []chan contract.ShellJobOutput {
	var watchers []chan contract.ShellJobOutput
	if state := s.sessions[id]; state != nil && state.watch != nil {
		w := state.watch
		for ch := range w.watchers {
			watchers = append(watchers, ch)
			delete(w.watchers, ch)
		}
		state.watch = nil
	}
	return watchers
}

func (s *Service) ShellWatch(ctx context.Context, id contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error) {
	session, open, err := s.store.OpenSession(ctx, id)
	if err != nil {
		return nil, failure(err)
	}
	if !open {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return nil, failure(err)
	}
	handle, state, ok := s.lockSession(id)
	if !ok {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	defer handle.Unlock()
	s.mu.Lock()
	if b.State != contract.BoxReady || handle.Channel() == nil {
		s.mu.Unlock()
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	channel := handle.Channel()
	w := state.watch
	if w == nil {
		w = &shellWatch{watchers: make(map[chan contract.ShellJobOutput]struct{})}
		state.watch = w
	}
	s.shellGeneration++
	w.generation = s.shellGeneration
	ch := make(chan contract.ShellJobOutput, 128)
	w.watchers[ch] = struct{}{}
	s.mu.Unlock()
	channel.Wake()
	go func() {
		<-ctx.Done()
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		s.mu.Lock()
		if _, ok := w.watchers[ch]; !ok {
			s.mu.Unlock()
			return
		}
		delete(w.watchers, ch)
		wake := len(w.watchers) == 0 && handle.Channel() == channel
		s.mu.Unlock()
		close(ch)
		if wake {
			channel.Wake()
		}
	}()
	return ch, nil
}

func (s *Service) ShellOutput(_ context.Context, caller contract.Principal, req contract.ShellOutputRequest) (contract.ShellOutputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ShellOutputResponse{}, forbidden()
	}
	handle, state, ok := s.lookupSession(caller.Session)
	if !ok {
		return contract.ShellOutputResponse{}, nil
	}
	defer handle.Unlock()
	s.mu.Lock()
	w := state.watch
	if w == nil || w.generation != req.Generation {
		s.mu.Unlock()
		return contract.ShellOutputResponse{}, nil
	}
	var watchers []chan contract.ShellJobOutput
	for ch := range w.watchers {
		watchers = append(watchers, ch)
	}
	s.mu.Unlock()
	var dropped []chan contract.ShellJobOutput
watcher:
	for _, ch := range watchers {
		for _, job := range req.Jobs {
			select {
			case ch <- job:
			default:
				dropped = append(dropped, ch)
				continue watcher
			}
		}
	}
	s.mu.Lock()
	for _, ch := range dropped {
		delete(w.watchers, ch)
	}
	channel := handle.Channel()
	wake := len(w.watchers) == 0 && channel != nil
	s.mu.Unlock()
	for _, ch := range dropped {
		close(ch)
	}
	if wake {
		channel.Wake()
	}
	return contract.ShellOutputResponse{}, nil
}
