package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/machines"
)

// A stop cannot confirm that a container is gone when the daemon is not
// reachable, so the node stop fails. Fake docker and sudo stand in for the
// unavailable daemon, and PATH holds only them.
func TestStopScriptFailsWhenDaemonUnavailable(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"docker", "sudo"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", StopScript, "stop", t.TempDir(), "dyna-job-job_abc.service", "dyna-job-job_abc")
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not confirmed stopped") {
		t.Fatalf("stop with no daemon = %v, output %q; want a failed stop", err, out)
	}
}

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

// A job command has no terminal, so git must not open an editor and hang. The
// script exports the editor variables as true, so git keeps the message it has.
func TestJobScriptSetsNoEditor(t *testing.T) {
	script := jobScript(machines.Machine{SSH: "aria@arch"}, Job{Command: []string{"true"}}, Resources{CPUs: 1, MemoryMiB: 512}, "")
	if want := "export EDITOR=true GIT_EDITOR=true VISUAL=true; "; !strings.Contains(script, want) {
		t.Fatalf("job script lacks %q:\n%s", want, script)
	}
}
