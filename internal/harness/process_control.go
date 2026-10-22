package harness

import (
	"context"
	"dyna-agent/internal/processguard"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func validateWorkingDirectory(cwd string) error {
	if cwd == "" {
		return nil
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return &workingDirectoryError{dir: cwd, cause: err}
	}
	if !info.IsDir() {
		return &workingDirectoryError{dir: cwd, cause: errors.New("not a directory")}
	}
	if err := syscall.Access(cwd, 1); err != nil {
		return &workingDirectoryError{dir: cwd, cause: err}
	}
	return nil
}

// Tests can extend this grace without changing the production flush window.
var journalInterruptGrace = 750 * time.Millisecond

// interruptForContinuation gives the CLI time to persist its exact session
// before a journal or steering continuation. A completion already waiting in
// done wins and can still be continued without interrupting it.
func interruptForContinuation(ctx context.Context, cmd *exec.Cmd, done <-chan error) (error, bool) {
	select {
	case runErr := <-done:
		return runErr, false
	default:
	}

	if err := signalProcessGroup(cmd, syscall.SIGINT); err != nil {
		if err == os.ErrProcessDone {
			return <-done, false
		}
		return stopProcess(cmd, done, err), false
	}
	if runErr, stopped, canceled := waitForProcess(ctx, done, journalInterruptGrace); stopped || canceled {
		if canceled {
			return stopCanceledProcess(cmd, done), false
		}
		return runErr, true
	}

	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err), true
	}
	if runErr, stopped, canceled := waitForProcess(ctx, done, journalTerminateGrace); stopped || canceled {
		if canceled {
			return stopCanceledProcess(cmd, done), false
		}
		return runErr, true
	}

	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err), true
	}
	return <-done, true
}

func stopCanceledProcess(cmd *exec.Cmd, done <-chan error) error {
	select {
	case runErr := <-done:
		return runErr
	default:
	}
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err)
	}
	if runErr, stopped, _ := waitForProcess(context.Background(), done, cancelTerminateGrace); stopped {
		return runErr
	}
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return stopProcess(cmd, done, err)
	}
	return <-done
}

func stopProcess(cmd *exec.Cmd, done <-chan error, signalErr error) error {
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil && err != os.ErrProcessDone {
		return fmt.Errorf("signal worker: %v; kill worker: %w", signalErr, err)
	}
	runErr := <-done
	if runErr != nil {
		return fmt.Errorf("signal worker: %v; worker exit: %w", signalErr, runErr)
	}
	return signalErr
}

func waitForProcess(ctx context.Context, done <-chan error, delay time.Duration) (runErr error, stopped, canceled bool) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case runErr = <-done:
		return runErr, true, false
	case <-ctx.Done():
		return nil, false, true
	case <-timer.C:
		return nil, false, false
	}
}

var guardedCommands sync.Map // *exec.Cmd -> *processguard.Controller; private runtimes only.

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if value, ok := guardedCommands.Load(cmd); ok {
		controller := value.(*processguard.Controller)
		err := controller.Signal(signal)
		if errors.Is(err, processguard.ErrGroupEnded) {
			return os.ErrProcessDone
		}
		return err
	}
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// withWorkingDirectoryEnv points PWD (and clears OLDPWD) at the worker's
// working directory. cmd.Dir changes the real cwd but leaves the inherited
// PWD variable at dyna's own directory, and some harnesses (opencode) trust
// $PWD over getcwd — which silently ran every worker in the wrong directory.
func withWorkingDirectoryEnv(env []string, cwd string) []string {
	if cwd == "" {
		return env
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	out := env[:0:0]
	for _, pair := range env {
		key := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			key = pair[:i]
		}
		if key == "PWD" || key == "OLDPWD" {
			continue
		}
		out = append(out, pair)
	}
	return append(out, "PWD="+abs)
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, pair := range base {
		key := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			key = pair[:i]
		}
		if _, replaced := overrides[key]; !replaced {
			out = append(out, pair)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}
