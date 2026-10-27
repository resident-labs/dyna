//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func (s *supervisor) openApp(ctx context.Context, r Request) (Opened, error) {
	intent, err := RequiredStatus(r.Status)
	if err != nil {
		return Opened{}, err
	}
	if err := ValidateAppCommand(r.Command); err != nil {
		return Opened{}, err
	}
	if r.TimeoutMS <= 0 {
		return Opened{}, fmt.Errorf("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.TimeoutMS)*time.Millisecond)
	defer cancel()
	select {
	case s.inputGate <- struct{}{}:
		defer func() { <-s.inputGate }()
	case <-ctx.Done():
		return Opened{}, ctx.Err()
	}
	// Subscribe before starting the process so a fast window cannot be missed.
	events, err := NewClient(s.meta.SessionID).Subscribe(ctx)
	if err != nil {
		return Opened{}, err
	}
	s.mu.Lock()
	s.recordIntent(intent)
	s.mu.Unlock()
	if _, _, _, _, _, err := s.agentTarget(ctx); err != nil {
		return Opened{}, err
	}
	s.mu.Lock()
	if s.compositor.alive() {
		s.readApps()
	}
	before := append([]App{}, s.state.Apps...)
	s.mu.Unlock()
	env, err := Environment(s.meta, os.Environ())
	if err != nil {
		return Opened{}, err
	}
	env = mergeEnvironment(env, map[string]string{"DYNA_COMPUTER_PROCESS": s.meta.SessionID})
	log := filepath.Join(Dir(s.meta.SessionID), fmt.Sprintf("app-%d.log", time.Now().UnixNano()))
	p, err := startProcess(env, log, r.Command...)
	if err != nil {
		return Opened{}, fmt.Errorf("open %q: %w (log: %s)", r.Command[0], err, log)
	}
	s.mu.Lock()
	if info, ok := processInfo(p.cmd.Process.Pid); ok {
		s.apps[info.pid] = info.start
	}
	s.attachCgroup(p.cmd.Process.Pid)
	s.lastActivity = time.Now()
	s.mu.Unlock()
	return waitForApp(ctx, p, events, before, log)
}

// waitForApp waits for a new window and stops the launch group on failure,
// even when the launcher has already exited.
func waitForApp(ctx context.Context, p *process, events <-chan Event, before []App, log string) (Opened, error) {
	command := p.cmd.Args[0]
	ready := false
	defer func() {
		if !ready {
			// The launcher can exit while its children still own this group.
			// Kill the launch group even when process.stop sees a dead leader.
			_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
			p.stop()
		}
	}()
	exited := p.done
	for {
		select {
		case <-ctx.Done():
			return Opened{}, noWindowError(command, ctx.Err(), log)
		case <-exited:
			if p.err != nil {
				return Opened{}, fmt.Errorf("open %q exited before a window appeared: %w (log: %s)", command, p.err, log)
			}
			// A successful launcher may leave its child to create the window.
			exited = nil
		case event, ok := <-events:
			if !ok {
				return Opened{}, eventsClosedError(ctx, command, log)
			}
			if event.Status == nil {
				continue
			}
			counts := map[string]int{}
			for _, app := range before {
				counts[app.AppID]++
			}
			for _, app := range event.Status.Apps {
				if counts[app.AppID] > 0 {
					counts[app.AppID]--
					continue
				}
				ready = true
				return Opened{PID: p.cmd.Process.Pid, Log: log, App: app}, nil
			}
		}
	}
}

func noWindowError(command string, cause error, log string) error {
	return fmt.Errorf("open %q: no new window before timeout or cancellation: %w (log: %s)", command, cause, log)
}

// eventsClosedError explains why the event stream ended. The stream closes
// with ctx, so a closed stream after the deadline is the timeout itself.
func eventsClosedError(ctx context.Context, command, log string) error {
	if err := ctx.Err(); err != nil {
		return noWindowError(command, err, log)
	}
	return fmt.Errorf("computer event connection closed (log: %s)", log)
}
