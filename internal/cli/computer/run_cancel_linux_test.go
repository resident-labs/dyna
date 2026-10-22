//go:build linux

package computer

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestComputerRunSignalHelper(t *testing.T) {
	if os.Getenv("DYNA_COMPUTER_RUN_SIGNAL_HELPER") != "1" {
		t.Skip("subprocess only")
	}
	c := NewCommand()
	c.SilenceErrors, c.SilenceUsage = true, true
	c.SetIn(strings.NewReader(""))
	c.SetOut(os.Stdout)
	c.SetErr(os.Stderr)
	args := []string{"run", "--status", "Checking the foreground command"}
	if os.Getenv("DYNA_COMPUTER_RUN_SIGNAL_TTY") == "1" {
		args = append(args, "--tty")
	}
	c.SetArgs(append(args, "--", "sh", "-c", `sleep 30 & printf '%s %s
' "$$" "$!"; wait`))
	if os.Getenv("DYNA_COMPUTER_RUN_TERMINAL_INPUT") == "1" {
		c.SetIn(os.Stdin)
		c.SetArgs([]string{"run", "--status", "Checking the foreground command", "--", "sh", "-c", `printf '%s
' "$$"; read value; printf 'read:%s
' "$value"`})
	}
	err := c.Execute()
	if err == nil {
		os.Exit(0)
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	os.Exit(1)
}

func TestComputerRunSignalsStopShellAndChild(t *testing.T) {
	for _, tc := range []struct {
		sig syscall.Signal
		tty bool
	}{{syscall.SIGTERM, false}, {syscall.SIGINT, false}, {syscall.SIGTERM, true}, {syscall.SIGINT, true}} {
		sig := tc.sig
		t.Run(fmt.Sprintf("%s/tty=%t", sig, tc.tty), func(t *testing.T) {
			ctx := computerRunFixture(t)
			helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestComputerRunSignalHelper$")
			helper.Env = append(os.Environ(), "DYNA_COMPUTER_RUN_SIGNAL_HELPER=1")
			if tc.tty {
				helper.Env = append(helper.Env, "DYNA_COMPUTER_RUN_SIGNAL_TTY=1")
			}
			helper.Stderr = os.Stderr
			out, err := helper.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			defer helper.Process.Kill()
			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil {
				helper.Wait()
				t.Fatal(err)
			}
			var shell, child int
			if n, err := fmt.Sscan(line, &shell, &child); err != nil || n != 2 {
				t.Fatalf("child PIDs: %q %v", line, err)
			}
			defer unix.Kill(shell, unix.SIGKILL)
			defer unix.Kill(child, unix.SIGKILL)
			descriptors := []int{}
			for _, pid := range []int{shell, child} {
				fd, err := unix.PidfdOpen(pid, 0)
				if err != nil {
					t.Fatal(err)
				}
				descriptors = append(descriptors, fd)
				defer unix.Close(fd)
			}
			if err := helper.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := helper.Wait(); err == nil {
				t.Fatal("signal returned successful exit")
			}
			for i, fd := range descriptors {
				fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
				n, err := unix.Poll(fds, 2000)
				if err != nil || n != 1 {
					t.Fatalf("job process %d survived %s: poll %d, %v", []int{shell, child}[i], sig, n, err)
				}
			}
		})
	}
}
