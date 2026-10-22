//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// startBrowser uses the same shim as interactive and automation browser calls.
// It waits outside mu because the shim calls browser.prepare through the daemon.
func (s *supervisor) startBrowser(ctx context.Context) (Browser, error) {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if err := s.start(); err != nil {
		return Browser{}, err
	}
	s.mu.Lock()
	var launched *process
	var err error
	if len(profileBrowserPIDs(s.meta.SessionID)) == 0 {
		var env []string
		env, err = Environment(s.meta, os.Environ())
		if err == nil {
			launched, err = startProcess(env, filepath.Join(Dir(s.meta.SessionID), "browser.log"), chromiumWrapper(s.meta.SessionID), "about:blank")
		}
		if err == nil {
			s.attachCgroup(launched.cmd.Process.Pid)
		}
	}
	s.mu.Unlock()
	if err != nil {
		return Browser{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		endpoint, err := browserEndpoint(s.meta.SessionID)
		if err == nil && browserCommand(bounded, s.meta.SessionID, "Browser.getVersion", nil) == nil {
			s.signIn(bounded, launched)
			return Browser{Endpoint: endpoint}, nil
		}
		var exited <-chan struct{}
		if launched != nil {
			exited = launched.done
		}
		select {
		case <-bounded.Done():
			return Browser{}, fmt.Errorf("wait for Chromium endpoint: %w", bounded.Err())
		case <-exited:
			return Browser{}, fmt.Errorf("Chromium exited before its endpoint was ready: %v", launched.err)
		case <-tick.C:
		}
	}
}

// signIn gives the browser a short-lived device for the installed app, and opens the app in a new browser.
// A failure is logged, not returned: a signed-out browser still works.
func (s *supervisor) signIn(ctx context.Context, launched *process) {
	id := s.meta.SessionID
	if err := signInBrowser(ctx, id); err != nil {
		logSignInFailure(id, err)
		return
	}
	if launched == nil {
		return
	}
	if err := openBrowserURLs(ctx, id, []string{browserAppURL}); err != nil {
		logSignInFailure(id, err)
	}
}
