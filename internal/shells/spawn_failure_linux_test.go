package shells

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// StartSupervisor starts the supervisor, then fails to record its PID. The
// supervisor must not be left running with nothing able to find it.
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
	exe := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexec sleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := StartSupervisor(exe, id); err == nil {
		t.Fatal("StartSupervisor reported success although the PID could not be recorded")
	}
	// Children of this test process: the supervisor must not be one of them.
	entries, _ := filepath.Glob("/proc/self/task/*/children")
	for _, e := range entries {
		raw, _ := os.ReadFile(e)
		for _, f := range strings.Fields(string(raw)) {
			if cmdline, _ := os.ReadFile("/proc/" + f + "/cmdline"); strings.Contains(string(cmdline), "sleep") || strings.Contains(string(cmdline), exe) {
				t.Errorf("supervisor process %s still runs: %q", f, cmdline)
			}
		}
	}
}
