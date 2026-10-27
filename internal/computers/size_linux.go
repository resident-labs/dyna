//go:build linux

package computers

import (
	"context"
	"fmt"
	"time"
)

func (s *supervisor) resize(ctx context.Context, r Request) (Status, error) {
	if r.Size == nil {
		return Status{}, fmt.Errorf("size is required")
	}
	if err := r.Size.Validate(); err != nil {
		return Status{}, err
	}
	size := r.Size.Aligned()
	intent, err := RequiredStatus(r.Status)
	if err != nil {
		return Status{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
	s.mu.Lock()
	s.recordIntent(intent)
	s.mu.Unlock()
	for {
		comp, _, _, _, _, err := s.agentTarget(ctx)
		if err != nil {
			return Status{}, err
		}
		s.captureMu.Lock()
		s.mu.Lock()
		unlock := func() { s.mu.Unlock(); s.captureMu.Unlock() }
		// A launch may already have captured the old dimensions. Finish that
		// launch first, then resize its output instead of changing only metadata.
		if s.starting != nil {
			done := s.starting.done
			unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return Status{}, ctx.Err()
			}
		}
		if s.compositor.alive() && (s.compositor != comp || s.state.Control != ControlAgent) {
			unlock()
			continue
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return Status{}, err
		}
		if s.compositor.alive() {
			if err := setOutputSize(ctx, s.desktopEnv(), size.Width, size.Height, s.state.RefreshHz, liveOutputTimeout); err != nil {
				unlock()
				return Status{}, err
			}
		}
		s.state.Width, s.state.Height = size.Width, size.Height
		s.lastActivity = time.Now()
		s.record("status", nil)
		state := *statusCopy(s.state)
		unlock()
		return state, nil
	}
}
