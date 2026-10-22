package processguard

import (
	"context"
	"dyna-agent/internal/childenv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Process describes a single bounded child. Execute is replaceable in tests.
type Process struct {
	Dir     string
	Args    []string
	Env     []string
	Timeout time.Duration
	Output  io.Writer
	Stderr  io.Writer
	// Bind commits launch metadata while the existing native gate is closed.
	Bind func(context.Context, Reference) error
}
type Executor func(context.Context, Process) error

// Execute kills the process group on cancellation, including command descendants.
// No lock files are passed through ExtraFiles. Clean marks inherited lock
// descriptors close-on-exec before launching the child.
func Execute(ctx context.Context, p Process) error {
	if len(p.Args) == 0 || p.Timeout <= 0 {
		return fmt.Errorf("process requires a command and a positive timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	cmd := Command(ctx, p.Args[0], p.Args[1:]...)
	cmd.Dir, cmd.Env = p.Dir, p.Env
	cmd.Stdout, cmd.Stderr = p.Output, p.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = p.Output
	}

	var err error
	if p.Bind == nil {
		err = childenv.Clean(cmd).Run()
	} else {
		var guarded *Controller
		guarded, err = Wrap(cmd)
		if err == nil {
			err = guarded.SetCancelSignal(syscall.SIGKILL)
		}
		if err == nil {
			err = guarded.Start(func(ref Reference) error { return p.Bind(ctx, ref) })
		}
		if err == nil {
			err = guarded.Wait()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Command cancels the whole process group and bounds inherited-pipe drains.
func Command(ctx context.Context, executable string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	return cmd
}

// MetadataEnv removes session stamps and Git routing from child commands.
func MetadataEnv() []string {
	var env []string
	hasGoFlags := false
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "MUSE_SESSION_ID":
			continue
		}
		if strings.HasPrefix(key, "DYNA_") || strings.HasPrefix(key, "GIT_") {
			continue
		}
		if key == "GOFLAGS" {
			_, flags, _ := strings.Cut(entry, "=")
			entry = "GOFLAGS=" + childenv.Trimpath(flags)
			hasGoFlags = true
		}
		env = append(env, entry)
	}
	if !hasGoFlags {
		env = append(env, "GOFLAGS=-trimpath")
	}
	return env
}
