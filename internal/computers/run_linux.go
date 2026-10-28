//go:build linux

package computers

import (
	"context"
	"time"
)

// prepareRun records intent and observes the same input ownership as GUI
// actions. The CLI owns the foreground process and its terminal streams.
func (s *supervisor) prepareRun(ctx context.Context, r Request) (Meta, error) {
	intent, err := RequiredStatus(r.Status)
	if err != nil {
		return Meta{}, err
	}
	leaseCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return Meta{}, ctx.Err()
	}
	s.mu.Lock()
	s.recordIntent(intent)
	s.mu.Unlock()
	if _, _, _, _, _, err := s.agentTarget(ctx); err != nil {
		return Meta{}, err
	}
	connection := ConnectionID(leaseCtx)
	s.mu.Lock()
	s.runs[connection] = struct{}{}
	s.state.Runs = len(s.runs)
	meta := s.meta
	s.record("runs", nil)
	s.mu.Unlock()
	go func() {
		<-leaseCtx.Done()
		s.mu.Lock()
		delete(s.runs, connection)
		s.state.Runs = len(s.runs)
		s.record("runs", nil)
		s.mu.Unlock()
	}()
	return meta, nil
}
