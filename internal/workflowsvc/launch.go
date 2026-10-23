package workflowsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/planclient"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
)

// ForegroundOptions forwards the worker's CLI presentation and waits for it.
type ForegroundOptions struct {
	Stdout, Stderr io.Writer
	Output         string
	Quiet          bool
	// StopWaiting lets a second interrupt leave cooperative cleanup running.
	StopWaiting <-chan struct{}
}

// ErrWaitInterrupted means the caller stopped waiting while cleanup continues.
var ErrWaitInterrupted = errors.New("stopped waiting for workflow cleanup")

// LaunchOptions selects the executable and complete child environment.
// A nil Env inherits the current process environment.
type LaunchOptions struct {
	Executable string
	Env        []string
	OnWarning  func(string)
	Foreground *ForegroundOptions
}

// Launch stages explicit options and starts the registered workflow runner.
// Detached launches return after registration; foreground launches join the worker.
func Launch(ctx context.Context, opts Options, launch LaunchOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := runstore.NewID()
	dir := filepath.Join(runstore.RunsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if opts.Name == "" {
		opts.Name = MetaName(opts.ScriptSrc, opts.ScriptPath)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.js"), []byte(opts.ScriptSrc), 0o600); err != nil {
		return "", err
	}
	data, err := json.Marshal(opts)
	if err != nil {
		return "", err
	}
	optionsPath := filepath.Join(dir, "launch.json")
	if err := os.WriteFile(optionsPath, data, 0o600); err != nil {
		return "", err
	}
	var log *os.File
	if launch.Foreground == nil {
		// Detached callers need their plan before returning. Foreground execution
		// keeps plan errors and warnings in Execute's existing run lifecycle.
		if _, err := plan.Ensure(dir, opts.ScriptSrc, plan.Options{WriteAncestry: planclient.StampAncestry, SourcePath: opts.ScriptPath, WorkDir: opts.WorkDir, WorkflowDir: opts.WorkflowDir, ResumedFrom: opts.ResumeFrom}); err != nil {
			if errors.Is(err, plan.ErrAncestry) {
				return "", err
			}
			if launch.OnWarning != nil {
				launch.OnWarning("plan persist: " + err.Error())
			}
		}
		log, err = os.Create(filepath.Join(dir, "daemon.log"))
		if err != nil {
			return "", err
		}
		defer log.Close()
	}
	if launch.Executable == "" {
		launch.Executable, err = fsutil.RunningExecutable()
		if err != nil {
			return "", err
		}
	}
	cmd := exec.Command(launch.Executable, "run-worker", optionsPath)
	cmd.Env = launch.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	detached := "1"
	cmd.Stdout, cmd.Stderr = log, log
	if foreground := launch.Foreground; foreground != nil {
		detached = "0"
		if foreground.Output != "" {
			cmd.Args = append(cmd.Args, "--output", foreground.Output)
		}
		if foreground.Quiet {
			cmd.Args = append(cmd.Args, "--quiet")
		}
		cmd.Stdout, cmd.Stderr = foreground.Stdout, foreground.Stderr
		// With no CommandContext watcher, WaitDelay bounds only pipe drains
		// after process exit. It never interrupts cooperative finalization.
		cmd.WaitDelay = time.Second
	}
	cmd.Env = append(cmd.Env, "DYNA_RUN_ID="+id, "DYNA_RUN_DETACHED="+detached)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// A workflow run acts as authority's worker role, never as a session.
	record := callerid.Launch{DataDir: store.DataDir(), Name: id, Actor: callerid.Identity{Kind: callerid.Worker, Run: id}, Launcher: "workflow"}
	started, ended := make(chan error, 1), make(chan error, 1)
	go func() {
		if launch.Foreground != nil {
			// Keep the creating OS thread alive through Wait, including after a
			// second interrupt lets the CLI stop waiting for finalization.
			unlock := foregroundParent(cmd)
			defer unlock()
		}
		err := callerid.Start(ctx, childenv.Clean(cmd), record)
		if err == nil && launch.Foreground == nil {
			_ = cmd.Process.Release()
		}
		started <- err
		if err == nil && launch.Foreground != nil {
			ended <- cmd.Wait()
		}
	}()
	if err := <-started; err != nil {
		return "", err
	}
	if launch.Foreground == nil {
		return id, nil
	}
	return id, waitForeground(ctx, cmd, ended, launch.Foreground.StopWaiting)
}

func waitForeground(ctx context.Context, cmd *exec.Cmd, ended <-chan error, stopWaiting <-chan struct{}) error {
	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
	}
	// Prefer completion when it raced with cancellation.
	select {
	case err := <-ended:
		return err
	default:
	}
	signalErr := cmd.Process.Signal(syscall.SIGTERM)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	select {
	case err := <-ended:
		if signalErr != nil {
			return fmt.Errorf("signal workflow coordinator: %w", signalErr)
		}
		return err
	case <-stopWaiting:
		// A finished child still wins over a second interrupt.
		select {
		case err := <-ended:
			return err
		default:
			return ErrWaitInterrupted
		}
	}
}
