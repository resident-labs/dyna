//go:build linux

package procident

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"testing"
)

// The real /proc parses, so the field count matches this kernel.
func TestStartTimeOfThisProcess(t *testing.T) {
	self, err := StartTime(os.Getpid())
	if err != nil || self == 0 {
		t.Fatalf("own process start time = %d, %v; want a non-zero field 22", self, err)
	}
	if again, err := StartTime(os.Getpid()); err != nil || again != self {
		t.Fatalf("second read = %d, %v; want the same start time %d", again, err, self)
	}
	stat, err := Read("/proc", os.Getpid())
	if err != nil || stat.StartTime != self || stat.PID != os.Getpid() || stat.ParentPID != os.Getppid() {
		t.Fatalf("Read = %+v, %v; want pid %d under %d with start %d", stat, err, os.Getpid(), os.Getppid(), self)
	}
}

// A dead process no longer has a start time, so its recorded PID cannot be
// mistaken for a live process.
func TestStartTimeOfAFinishedProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if started, err := StartTime(pid); !errors.Is(err, fs.ErrNotExist) || started != 0 {
		t.Fatalf("start time of a finished process = %d, %v; want 0 and a missing file", started, err)
	}
}
