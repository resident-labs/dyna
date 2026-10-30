// Package slot limits concurrent local test commands on this host.
package slot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/cli/exitcode"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/settings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const heldEnv = "DYNA_TEST_SLOT_HELD"

func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "slot", Short: "Limit concurrent local test runs"}
	run := &cobra.Command{
		Use: "run -- command [args...]", Short: "Run a command in a host-wide test slot",
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "dyna slot: a command is required")
				return exitcode.New(1, "")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runCommand(args, home.LaunchEnv(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "dyna slot:", err)
				return exitcode.New(1, "")
			}
			if code != 0 {
				return exitcode.New(code, "")
			}
			return nil
		},
	}
	root.AddCommand(run)
	return root
}

func runCommand(args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cfg, err := settings.Load("")
	if err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	var lock *os.File
	if cfg.TestSlots.Slots > 0 && os.Getenv(heldEnv) != "1" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if !filepath.IsAbs(runtimeDir) {
			return 1, fmt.Errorf("XDG_RUNTIME_DIR must be an absolute path")
		}
		dir := filepath.Join(runtimeDir, "dyna", "test-slots")
		ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		lock, err = acquire(ctx, dir, cfg.TestSlots.Slots, os.Getenv("DYNA_SESSION"), stderr, waitTick)
		if err != nil {
			return 1, err
		}
		defer lock.Close()
	}
	child := exec.Command(args[0], args[1:]...)
	child.Stdin, child.Stdout, child.Stderr = stdin, stdout, stderr
	child.Env = appendEnv(env, heldEnv, "1")
	// Notify separately so a signal that arrives while the command runs reaches it.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := child.Start(); err != nil {
		return 1, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = child.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err = child.Wait()
	close(done)
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 1, err
}

func appendEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}

func waitTick(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// acquire keeps each lock file open until the test exits. Lock files are never
// removed, because unlinking a locked file would create a second lock domain.
func acquire(ctx context.Context, dir string, count int, session string, notice io.Writer, wait func(context.Context) error) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if session == "" {
		session = "unknown"
	}
	session = strings.Join(strings.Fields(session), " ")
	if len(session) > 128 {
		session = session[:128]
	}
	announced := false
	for {
		holders := make([]string, 0, count)
		for i := 0; i < count; i++ {
			path := filepath.Join(dir, strconv.Itoa(i)+".lock")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				return nil, err
			}
			err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if err == nil {
				if err = f.Truncate(0); err == nil {
					_, err = f.WriteAt([]byte(session), 0)
				}
				if err != nil {
					f.Close()
					return nil, err
				}
				return f, nil
			}
			if !errors.Is(err, unix.EWOULDBLOCK) {
				f.Close()
				return nil, err
			}
			b, _ := io.ReadAll(io.LimitReader(f, 128))
			f.Close()
			holder := strings.TrimSpace(string(b))
			if holder == "" {
				holder = "unknown"
			}
			holders = append(holders, holder)
		}
		if !announced {
			fmt.Fprintf(notice, "waiting for a test slot: %d of %d in use, held by %s\n", count, count, strings.Join(holders, ", "))
			announced = true
		}
		if err := wait(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("timed out after 30 minutes waiting for a test slot")
			}
			return nil, fmt.Errorf("waiting for a test slot: %w", err)
		}
	}
}
