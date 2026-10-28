package jobs

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Cancelling a command kills its whole process group and drains its output
// pipes. A child that outlived the command's shell keeps the pipe open; the
// wait must not block on it, or the run never reaches its cleanup.
func TestCommandCancelsTheProcessGroupAndDrainsItsPipes(t *testing.T) {
	hold := filepath.Join(t.TempDir(), "hold")
	if out, err := exec.Command("mkfifo", hold).CombinedOutput(); err != nil {
		t.Skipf("mkfifo is unavailable: %v: %s", err, out)
	}
	writer, err := os.OpenFile(hold, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close() // the child reads this FIFO, so closing it ends the child
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, input := io.Pipe()
	ran := make(chan error, 1)
	go func() {
		// The shell starts a child that holds the output pipe, then exits.
		err := RunCommand(ctx, t.TempDir(), nil, input, input, "bash", "-c", `cat "$1" & printf 'ready\n'`, "command-test", hold)
		input.Close()
		ran <- err
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("the command's child did not hold the pipe: %q %v", line, err)
	}
	cancel()
	if err := <-ran; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command returned %v", err)
	}
}

// A command that exits while a child it started keeps the output pipe open is
// reported as ErrWaitDelay after the wait bound, and the command's process
// group is killed rather than left behind. The run and the reaper are given,
// so the decision is proven without a real wait or a real child.
func TestRunAndReapKillsTheGroupOfALeftoverChild(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Process = &os.Process{Pid: 4242}
	var killed []int
	err := runAndReapWith(func() error { return exec.ErrWaitDelay }, cmd, func(pid int) error {
		killed = append(killed, pid)
		return nil
	})
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("a leftover pipe holder was not reported: %v", err)
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("reaped groups %v, want 4242", killed)
	}
	// A command that failed for another reason is not reaped, so a real failure
	// keeps its exit status.
	killed = nil
	if err := runAndReapWith(func() error { return errors.New("exit status 1") }, cmd, func(pid int) error {
		killed = append(killed, pid)
		return nil
	}); err == nil || len(killed) != 0 {
		t.Fatalf("a failed command reaped groups %v", killed)
	}
}
