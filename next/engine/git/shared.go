package git

import (
	"context"
	"sync"
)

// shared lets callers with the same key share one run of slow work.
//
// A caller never takes a result that began before it asked. It joins the next run, which starts only when the current
// one ends, so a burst of any size takes at most two runs while one lane stays busy, and each caller still sees
// everything that happened before it asked. Keys run independently.
type shared struct {
	mu    sync.Mutex
	lanes map[any]*lane
}

type lane struct{ running, queued *run }

type run struct {
	done chan struct{}
	err  error
	// abandoned means the caller doing the work went away, so its error says nothing about the work.
	abandoned bool
}

// do returns the result of a run that started after this call. The caller may do the work itself, under its own context.
func (s *shared) do(ctx context.Context, key any, work func(context.Context) error) error {
	s.mu.Lock()
	if s.lanes == nil {
		s.lanes = make(map[any]*lane)
	}
	l := s.lanes[key]
	if l == nil {
		l = &lane{}
		s.lanes[key] = l
	}
	s.mu.Unlock()
	for {
		s.mu.Lock()
		joined := l.queued
		if joined == nil {
			joined = &run{done: make(chan struct{})}
			l.queued = joined
		}
		s.mu.Unlock()
		err := s.await(ctx, l, joined, work)
		if ctx.Err() != nil || err == nil || !joined.abandoned {
			return err
		}
	}
}

// await waits for joined to finish, and does its work if no one else has started it by the time its turn comes.
func (s *shared) await(ctx context.Context, l *lane, joined *run, work func(context.Context) error) error {
	for {
		s.mu.Lock()
		ahead := l.running
		if ahead == nil && l.queued == joined {
			l.running, l.queued = joined, nil
			s.mu.Unlock()
			joined.err = work(ctx)
			joined.abandoned = ctx.Err() != nil
			s.mu.Lock()
			l.running = nil
			s.mu.Unlock()
			close(joined.done)
			return joined.err
		}
		s.mu.Unlock()
		if ahead == nil {
			// joined left the queue and nothing is running, so it has ended or is about to close.
			ahead = joined
		}
		select {
		case <-ahead.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if ahead == joined {
			return joined.err
		}
	}
}
