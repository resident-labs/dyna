package shells

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// StartSupervisor starts the supervisor, then fails to record its PID. Neither
// the supervisor nor any process of its job, even one that ignores SIGTERM, may
// be left running with nothing able to find it.
func TestStartSupervisorStopsTheSupervisorWhenItsPIDCannotBeRecorded(t *testing.T) {
	id := testJob(t)
	dir, err := DefaultStore().dir(id)
	if err != nil {
		t.Fatal(err)
	}
	// A directory in the PID file's place makes the write fail.
	if err := os.Mkdir(filepath.Join(dir, supervisorPID), 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake supervisor starts a stamped child that ignores SIGTERM and tells
	// the test through a FIFO. StartSupervisor waits for that before it records
	// the PID, so the child exists when the recording fails.
	fifo := filepath.Join(t.TempDir(), "up")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "dyna")
	script := "#!/bin/sh\n" + JobEnv + `=$3 sh -c 'trap "" TERM; echo up > ` + fifo + `; exec sleep 600' &` + "\nwait\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	afterSupervisorLaunch = func() {
		if _, err := os.ReadFile(fifo); err != nil { // returns once the child is up
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterSupervisorLaunch = nil })
	if err := StartSupervisor(exe, id); err == nil {
		t.Fatal("StartSupervisor reported success although the PID could not be recorded")
	}
	if left := jobProcesses(id, map[int]pinned{}); len(left) != 0 {
		for _, p := range left {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
		t.Fatalf("processes of the job still run: %v", left)
	}
}
