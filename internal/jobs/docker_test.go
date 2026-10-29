package jobs

import (
	"strings"
	"testing"

	"dyna-agent/internal/machines"
)

// A systemd user manager that started before its user joined the docker group
// keeps the old groups, so every node falls back to passwordless sudo docker.
func TestJobScriptFallsBackToSudoDockerOnSSHNode(t *testing.T) {
	script := jobScript(machines.Machine{SSH: "aria@arch"}, Job{Image: "alpine", Command: []string{"true"}}, Resources{CPUs: 1, MemoryMiB: 512}, "")
	if !strings.Contains(script, "sudo -n docker") {
		t.Fatalf("an SSH node's job script has no sudo docker fallback:\n%s", script)
	}
}
