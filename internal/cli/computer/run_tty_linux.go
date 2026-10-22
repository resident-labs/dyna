//go:build linux

package computer

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func foregroundSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

// configureForeground returns a function that restores terminal ownership.
func configureForeground(cmd *exec.Cmd, tty bool, input io.Reader) func() {
	restore := func() {}
	if !tty {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			// The job group owns the terminal while it runs; otherwise its reads stop with SIGTTIN.
			cmd.SysProcAttr.Foreground, cmd.SysProcAttr.Ctty = true, 0
			fd, group := int(file.Fd()), syscall.Getpgrp()
			restore = func() {
				// A background group gets SIGTTOU when it changes the foreground group.
				signal.Ignore(syscall.SIGTTOU)
				defer signal.Reset(syscall.SIGTTOU)
				_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, group)
			}
		}
	}
	// PTY startup creates its own session and group with the same process ID.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return restore
}

func foregroundExitCode(err *exec.ExitError) int {
	if state, ok := err.Sys().(syscall.WaitStatus); ok && state.Signaled() {
		return 128 + int(state.Signal())
	}
	return err.ExitCode()
}

func runTTY(c *cobra.Command, cmd *exec.Cmd) error {
	size := &pty.Winsize{Rows: 24, Cols: 80}
	input, terminal := c.InOrStdin().(*os.File)
	terminal = terminal && term.IsTerminal(int(input.Fd()))
	if terminal {
		if actual, err := pty.GetsizeFull(input); err == nil {
			size = actual
		}
		old, err := term.MakeRaw(int(input.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(input.Fd()), old)
	}
	master, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return err
	}
	defer master.Close()
	// Pollable reads let the output drain finish after the child exits.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return err
	}
	if err := master.SetReadDeadline(time.Time{}); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return err
	}
	if terminal {
		changes := make(chan os.Signal, 1)
		signal.Notify(changes, syscall.SIGWINCH)
		defer signal.Stop(changes)
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-changes:
					_ = pty.InheritSize(input, master)
				}
			}
		}()
	}
	go func() {
		_, err := io.Copy(master, c.InOrStdin())
		if err == nil && !terminal {
			// Canonical mode needs one EOF to flush a partial line and another
			// to end input. A completed line only needs the first EOF.
			_, _ = master.Write([]byte{4, 4})
		}
	}()
	ended := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		// Drain final output, but do not wait for a background child to close the PTY.
		if e := master.SetReadDeadline(time.Now().Add(time.Second)); e != nil {
			master.Close()
		}
		ended <- err
	}()
	_, readErr := io.Copy(c.OutOrStdout(), master)
	if readErr != nil && !errors.Is(readErr, syscall.EIO) && !errors.Is(readErr, os.ErrDeadlineExceeded) && !errors.Is(readErr, os.ErrClosed) {
		_ = cmd.Cancel()
	}
	err = <-ended
	if err != nil {
		return err
	}
	if errors.Is(readErr, syscall.EIO) || errors.Is(readErr, os.ErrDeadlineExceeded) || errors.Is(readErr, os.ErrClosed) {
		return nil
	}
	return readErr
}
