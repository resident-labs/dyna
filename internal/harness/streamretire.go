package harness

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// These are cleanup graces AFTER a protocol result, not task timeouts.
// They are variables only so tests can give a slow host more time.
var (
	streamRetireExitGrace      = 250 * time.Millisecond
	streamRetireTerminateGrace = 250 * time.Millisecond
)

// retireClaudeProcess owns reaping even after successful protocol completion.
// EOF is only a transport boundary: harness-native background tasks may keep
// Claude alive and generate more results. Retire its entire process group before
// an exact-session follow-up can start. Expected signal exits are not task errors.
func retireClaudeProcess(cmd *exec.Cmd, done <-chan error) error {
	reaped := false
	wait := func(grace time.Duration) bool {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			if reaped && syscall.Kill(-cmd.Process.Pid, 0) == syscall.ESRCH {
				return true
			}
			select {
			case <-done:
				reaped = true
				done = nil
			case <-tick.C:
			case <-timer.C:
				return false
			}
		}
	}
	if wait(streamRetireExitGrace) {
		return nil
	}
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		// Still attempt SIGKILL and reap; don't abandon an owned process on failure.
		_ = signalProcessGroup(cmd, syscall.SIGKILL)
		if !reaped {
			<-done
		}
		return fmt.Errorf("retire Claude process group: %w", err)
	}
	if wait(streamRetireTerminateGrace) {
		return nil
	}
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("kill retiring Claude process group: %w", err)
	}
	if !reaped {
		<-done
	}
	return nil
}
