//go:build linux

package computers

import (
	"context"
	"fmt"
	"time"
)

// openBrowser shares the input gate and saved intent used by computer actions.
func (s *supervisor) openBrowser(ctx context.Context, r Request) (any, error) {
	intent, err := RequiredStatus(r.Status)
	if err != nil {
		return nil, err
	}
	if len(r.BrowserArgs) != 1 {
		return nil, fmt.Errorf("browser open requires one URL")
	}
	if err := validateBrowserURLs(r.BrowserArgs); err != nil {
		return nil, err
	}
	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	s.recordIntent(intent)
	s.mu.Unlock()
	waitctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if _, _, _, _, _, err := s.agentTarget(waitctx); err != nil {
		return nil, err
	}
	if _, err := s.startBrowser(waitctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := openBrowserURLs(waitctx, s.meta.SessionID, r.BrowserArgs); err != nil {
		return nil, err
	}
	return map[string]bool{"opened": true}, nil
}
