package harness

import (
	"os/exec"
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
