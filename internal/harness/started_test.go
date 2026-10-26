package harness

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

// Started hears each harness process's PID right after it starts, so a
// workflow agent's process can be recorded for its agent number.
func TestStartedHearsTheHarnessPID(t *testing.T) {
	var heard int
	cmd := exec.CommandContext(t.Context(), "/bin/true")
	wait, release, _, err := startGuarded(t.Context(), profile.Profile{}, cmd, "", nil, ExecutionOptions{Started: func(pid int) { heard = pid }})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_ = wait()
	if heard == 0 || heard != cmd.Process.Pid {
		t.Fatalf("Started heard %d, want the process's PID %d", heard, cmd.Process.Pid)
	}
}

// Cancelling a guarded command must reach every member of its process group and
// let them finish what they write. The fixture installs real signal handlers in
// a parent and a child process, so the test waits for a readiness line from the
// fixture and for the parent to exit; it never waits on a timer or on a shell
// trap to run, which is what made the old version flaky under load.
func TestGuardedCancelTerminatesDescendantAndFlushes(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// A hang guard only: the fixture reports a missed signal with exit 3.
	guarded, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	ready, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	cmd := exec.CommandContext(guarded, os.Args[0])
	cmd.Env = append(os.Environ(), envCancelHelper+"=parent", envCancelMarker+"="+dir)
	cmd.Stdout, cmd.Stderr = readyWriter, readyWriter
	wait, release, _, err := startGuarded(guarded, profile.Profile{Harness: profile.HarnessMock}, cmd, "", nil, ExecutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer signalProcessGroup(cmd, syscall.SIGKILL)
	_ = readyWriter.Close()
	if line, err := bufio.NewReader(ready).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("fixture readiness = %q, %v", line, err)
	}
	cancel()
	_ = wait()
	for _, name := range []string{"child-stopped", "parent-stopped"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "term" {
			t.Fatalf("%s: %q, %v", name, data, err)
		}
	}
}

const (
	// envCancelHelper makes the test binary act as one member of the
	// cancellation fixture: "parent" starts a "child" and waits for it.
	envCancelHelper = "DYNA_TEST_CANCEL_HELPER"
	// envCancelMarker names the directory the fixture writes its markers to.
	envCancelMarker = "DYNA_TEST_CANCEL_MARKER"
)

// cancelHelperMain is one member of the cancellation fixture. A process group
// with real signal handlers is the only way to prove that a cancel reaches
// every member: a shell trap only runs when the shell happens to be between
// commands, which is what made the old test flaky.
func cancelHelperMain(mode string) {
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	dir := os.Getenv(envCancelMarker)
	switch mode {
	case "child":
		// Ready means the handler is installed; the parent prints it.
		if f := os.NewFile(3, "ready"); f != nil {
			_, _ = f.WriteString("child-ready")
		}
		select {
		case <-terms:
			_ = os.WriteFile(filepath.Join(dir, "child-stopped"), []byte("term"), 0600)
			os.Exit(0)
		case <-time.After(10 * time.Second):
			os.Exit(3)
		}
	case "parent":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), envCancelHelper+"=child")
		readyRead, readyWrite, err := os.Pipe()
		if err != nil {
			os.Exit(3)
		}
		child.ExtraFiles = []*os.File{readyWrite}
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = readyWrite.Close()
		ready := make([]byte, len("child-ready"))
		if _, err := io.ReadFull(readyRead, ready); err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		exited := make(chan error, 1)
		go func() { exited <- child.Wait() }()
		select {
		case err := <-exited:
			if err != nil {
				os.Exit(3)
			}
		case <-time.After(10 * time.Second):
			os.Exit(3)
		}
		select {
		case <-terms:
			_ = os.WriteFile(filepath.Join(dir, "parent-stopped"), []byte("term"), 0600)
			os.Exit(0)
		case <-time.After(10 * time.Second):
			os.Exit(3)
		}
	}
	os.Exit(3)
}
