package harness

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

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

func TestGuardedCancelTerminatesDescendantAndFlushes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer readyWriter.Close()
	hold, holdWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	defer holdWriter.Close()
	dir := t.TempDir()
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	// Both shells flush on SIGTERM. The parent waits for the child, so its
	// exit also proves the whole process group received cancellation.
	script := `
trap 'wait; printf "parent-stopped\n"; exit 0' TERM
/bin/sh -c '
  trap '\''printf "child-stopped\n"; exit 0'\'' TERM
  printf "child-ready\n" >&3
  read -r held <&4
' &
wait
`
	p := profile.Profile{Harness: profile.HarnessCustom}
	cmd, err := harnessCommand(ctx, p, dir, []string{"/bin/sh", "-c", script}, ExecutionOptions{SkipComputer: true})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.ExtraFiles = []*os.File{readyWriter, hold}
	wait, release, _, err := startGuarded(ctx, p, cmd, "", nil, ExecutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer signalProcessGroup(cmd, syscall.SIGKILL)
	_ = readyWriter.Close()
	line, err := bufio.NewReader(ready).ReadString('\n')
	if err != nil || line != "child-ready\n" {
		t.Fatalf("child readiness = %q, %v", line, err)
	}
	cancel()
	_ = wait()
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "parent-stopped\n") || !strings.Contains(string(raw), "child-stopped\n") {
		t.Fatalf("cancellation did not flush the whole process group: %q", raw)
	}
}
