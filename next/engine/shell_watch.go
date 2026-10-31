package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

type shellWatch struct {
	generation uint64
	watchers   map[chan contract.ShellJobOutput]struct{}
}

func (s *Service) clearShellLocked(id contract.SessionID) {
	if w := s.shellWatches[id]; w != nil {
		for ch := range w.watchers {
			close(ch)
			delete(w.watchers, ch)
		}
		delete(s.shellWatches, id)
	}
}

func (s *Service) ShellWatch(ctx context.Context, id contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error) {
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return nil, failure(err)
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return nil, failure(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel := s.channels[id]
	if b.State != contract.BoxReady || channel == nil {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	w := s.shellWatches[id]
	if w == nil {
		w = &shellWatch{watchers: make(map[chan contract.ShellJobOutput]struct{})}
		s.shellWatches[id] = w
	}
	s.shellGeneration++
	w.generation = s.shellGeneration
	ch := make(chan contract.ShellJobOutput, 128)
	w.watchers[ch] = struct{}{}
	wakeChannel(channel)
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := w.watchers[ch]; !ok {
			return
		}
		delete(w.watchers, ch)
		close(ch)
		if len(w.watchers) == 0 && s.channels[id] == channel {
			wakeChannel(channel)
		}
	}()
	return ch, nil
}

func (s *Service) ShellOutput(_ context.Context, caller contract.Principal, req contract.ShellOutputRequest) (contract.ShellOutputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ShellOutputResponse{}, forbidden()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.shellWatches[caller.Session]; w != nil && w.generation == req.Generation {
		for _, job := range req.Jobs {
			for ch := range w.watchers {
				select {
				case ch <- job:
				default:
					close(ch)
					delete(w.watchers, ch)
				}
			}
		}
		if len(w.watchers) == 0 && s.channels[caller.Session] != nil {
			wakeChannel(s.channels[caller.Session])
		}
	}
	return contract.ShellOutputResponse{}, nil
}
