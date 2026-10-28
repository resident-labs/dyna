package jobs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
)

// keepActive marks a job as live for a slot script, as the job's keeper does.
func keepActive(t *testing.T, dir string) string {
	t.Helper()
	active := filepath.Join(dir, ".active")
	if err := os.WriteFile(active, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return active
}

// A waiting check takes the first slot that frees up, not one named slot: with
// every slot busy, the second poll finds slot 0 free and runs there, instead of
// queueing behind slot 1 while other slots stay idle.
func TestWaitingCheckTakesTheFirstFreeSlot(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	attempts := filepath.Join(dir, "attempts")
	// The scan's three attempts (slots 0, 1, 2) fail; after that only slot 0 is
	// free, so a wrapper that waits on a named slot never runs.
	fake := "#!/bin/sh\ncount=$(cat " + shellquote.Quote(attempts) + " 2>/dev/null || echo 0)\ncount=$((count + 1))\necho \"$count\" > " + shellquote.Quote(attempts) + "\n" +
		"case \"$(readlink /proc/self/fd/9 2>/dev/null)\" in *\"0.lock\") [ \"$count\" -gt 3 ] && exit 0 ;; esac\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := SlotScript(dir, 3, 1, keepActive(t, dir), 2*time.Second, `printf 'slot=%s port=%s\n' "$slot" "$(( 4226 + slot ))"`)
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "DYNA_CI_SLOT_POLL=0.01")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("wrapper: %v: %s", err, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "slot=0 port=4226" {
		t.Fatalf("the waiting check ran with %q, want slot 0 and its port", got)
	}
}

// A job's own script runs its command on a real shell: nothing outside the
// script prepares the job's folder, so the script must mark the job live
// before its slot wrapper checks for it.
func TestJobScriptRunsItsCommand(t *testing.T) {
	machine := machines.Machine{Name: "jobs-test-" + filepath.Base(t.TempDir())}
	t.Cleanup(func() { os.RemoveAll(NodeBase(machine.Name)) })
	job := Job{Dir: t.TempDir(), Command: []string{"echo", "hello from a job"}, Timeout: 10 * time.Second}
	out, err := exec.Command("bash", "-c", jobScript(machine, job, Resources{CPUs: 1, MemoryMiB: 512})).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "hello from a job" {
		t.Fatalf("job script = %q, %v; want the command's output and exit 0", out, err)
	}
}

// A container job hands its command to the container runtime: the script
// calls docker run with the image and the command, not docker alone.
func TestContainerJobScriptRunsDocker(t *testing.T) {
	machine := machines.Machine{Name: "jobs-test-" + filepath.Base(t.TempDir())}
	t.Cleanup(func() { os.RemoveAll(NodeBase(machine.Name)) })
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1\" = info ] && exit 0\necho \"docker $*\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	job := Job{Dir: t.TempDir(), Command: []string{"echo", "hi"}, Image: "alpine:3", Timeout: 10 * time.Second}
	cmd := exec.Command("bash", "-c", jobScript(machine, job, Resources{CPUs: 1, MemoryMiB: 512}))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "docker run ") || !strings.Contains(string(out), " alpine:3 ") {
		t.Fatalf("container job script = %q, %v; want one docker run of alpine:3", out, err)
	}
}
