package workflows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/exitcode"
	"dyna-agent/internal/workflowsvc"
)

func runForeground(c *cobra.Command, opts workflowsvc.Options, quiet bool, output string) error {
	ctx, cancel := context.WithCancel(c.Context())
	defer cancel()
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	stopWaiting, finished := make(chan struct{}), make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-interrupts:
			cancel()
		case <-finished:
			return
		}
		select {
		case <-interrupts:
			close(stopWaiting)
		case <-finished:
		}
	}()
	id, err := workflowsvc.Launch(ctx, opts, workflowsvc.LaunchOptions{Foreground: &workflowsvc.ForegroundOptions{
		Stdout: c.OutOrStdout(), Stderr: c.ErrOrStderr(), Output: output, Quiet: quiet, StopWaiting: stopWaiting,
	}})
	if errors.Is(err, workflowsvc.ErrWaitInterrupted) {
		return fmt.Errorf("stopped waiting for run %s; cleanup continues", id)
	}
	var failed *exec.ExitError
	if errors.As(err, &failed) && failed.ProcessState.Exited() {
		// The worker's Cobra root already printed its diagnostic to stderr.
		c.SilenceErrors, c.SilenceUsage = true, true
		return exitcode.New(failed.ExitCode(), failed.Error())
	}
	return err
}
