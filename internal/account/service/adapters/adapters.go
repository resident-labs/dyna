package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
)

var ErrUnsupported = errors.New("native capability unavailable")
var ErrProbe = errors.New("native discovery failed")
var ErrIdentity = errors.New("native account identity did not match")

// Models lists the harness's models. env is the launch environment of Dyna's
// private copy; nil inherits this process's environment.
func Models(ctx context.Context, harness, executable string, env []string) ([]Model, error) {
	switch harness {
	case "opencode":
		return modelsOpenCode(ctx, executable, env)
	case "codex":
		return modelsCodex(ctx, executable, env)
	case "claude", "claude-code":
		return modelsClaude(ctx, executable, env)
	case "muse":
		return modelsMuse(ctx, executable, env)
	case "grok":
		return modelsGrok(ctx, executable, env)
	case "pi":
		return modelsPi(ctx, executable, env)
	case "kimi-code":
		return modelsKimiCode(ctx, executable, env)
	}
	return nil, ErrUnsupported
}

// limitBuffer prevents a misbehaving harness from retaining unlimited output.
// It accepts excess writes so the child cannot block on a full stdout pipe.
type limitBuffer struct {
	b        bytes.Buffer
	overflow bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (4 << 20) - b.b.Len()
	if n > remaining {
		b.overflow = true
		if remaining > 0 {
			_, _ = b.b.Write(p[:remaining])
		}
	} else {
		_, _ = b.b.Write(p)
	}
	return n, nil
}

// run never includes stderr, command output, or arguments in returned errors.
// It wraps ErrProbe with the process outcome only, so the reason is safe to
// show the owner: why the probe failed, never what the harness printed.
func run(ctx context.Context, executable string, env []string, args ...string) ([]byte, error) {
	if executable == "" {
		return nil, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var out limitBuffer
	cmd.Stdout = &out
	if err := childenv.Clean(cmd).Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrProbe, probeFailure(ctx, err))
	}
	if out.overflow {
		return nil, fmt.Errorf("%w: the harness produced more output than Dyna reads", ErrProbe)
	}
	return out.b.Bytes(), nil
}

// probeFailure names the outcome of a failed child process: a timeout, an
// exit status, a signal, or a start failure. A deadline or cancellation is
// checked first, because the kill it triggers hides behind an ExitError.
// It reads only the context and error values, never child output.
func probeFailure(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "the harness did not finish in time"
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		return fmt.Sprintf("the harness exited with status %d", exit.ExitCode())
	case errors.As(err, &exit):
		return "the harness was terminated by a signal"
	default:
		return "the harness could not start"
	}
}
