package jobs

import (
	"strings"
	"testing"

	"dyna-agent/internal/machines"
)

// An image job's container has a known name, and its script traps an exit to
// stop that container, because timeout kills only the docker client.
func TestJobScriptNamesAndStopsContainer(t *testing.T) {
	script := jobScript(machines.Machine{SSH: "aria@arch"}, Job{ID: "job_abc", Image: "alpine", Command: []string{"true"}}, Resources{CPUs: 1, MemoryMiB: 512}, "")
	for _, want := range []string{"run --rm --init --name 'dyna-job-job_abc' ", "trap 'dyna_stop_container dyna-job-job_abc' EXIT"} {
		if !strings.Contains(script, want) {
			t.Fatalf("container job script lacks %q:\n%s", want, script)
		}
	}
}

// A systemd user manager that started before its user joined the docker group
// keeps the old groups, so every node falls back to passwordless sudo docker.
func TestJobScriptFallsBackToSudoDockerOnSSHNode(t *testing.T) {
	script := jobScript(machines.Machine{SSH: "aria@arch"}, Job{Image: "alpine", Command: []string{"true"}}, Resources{CPUs: 1, MemoryMiB: 512}, "")
	if !strings.Contains(script, "sudo -n docker") {
		t.Fatalf("an SSH node's job script has no sudo docker fallback:\n%s", script)
	}
}
