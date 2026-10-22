package subagent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestStopSignalHelper(t *testing.T) {
	if os.Getenv("GO_STOP_SIGNAL_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, stop := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
}

// The process must match both the session and turn. The successful signal
// uses a pidfd, so a process that later reuses its pid cannot receive it.
func TestStopSignalChecksTheExactRunner(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const id = "ses_20261021-000000-00000001"
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStopSignalHelper$", "--", "subagent", "_turn", id, "1")
	cmd.Env = append(os.Environ(), "GO_STOP_SIGNAL_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	wait := func(phase string) {
		t.Helper()
		guard := time.NewTimer(30 * time.Second)
		defer guard.Stop()
		select {
		case <-done:
		case <-guard.C:
			t.Fatalf("runner %s did not finish within 30 seconds", phase)
		}
	}
	t.Cleanup(func() { cancel(); wait("cleanup") })
	type readiness struct {
		line string
		err  error
	}
	ready := make(chan readiness, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ready <- readiness{line, err}
	}()
	guard := time.NewTimer(30 * time.Second)
	defer guard.Stop()
	select {
	case got := <-ready:
		if got.err != nil || got.line != "ready\n" {
			t.Fatalf("runner readiness = %q, %v", got.line, got.err)
		}
	case <-guard.C:
		t.Fatal("runner did not become ready within 30 seconds")
	}
	if err := signalRunner("another-session", 1, cmd.Process.Pid, syscall.SIGTERM); err == nil {
		t.Fatal("a signal for another session was accepted")
	}
	if err := signalRunner(id, 2, cmd.Process.Pid, syscall.SIGTERM); err == nil {
		t.Fatal("a signal for another turn was accepted")
	}
	if err := signalRunner(id, 1, cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	wait("SIGTERM exit")
	if waitErr != nil {
		t.Fatalf("the runner did not handle SIGTERM: %v", waitErr)
	}
}
