package runstore

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"dyna-agent/internal/procident"
)

// createRun makes one run in the test's store; the caller sets
// XDG_DATA_HOME once so every run lands in the same dyna.db.
func createRun(t *testing.T, id string) *Run {
	t.Helper()
	t.Setenv("DYNA_RUN_ID", id)
	t.Setenv(SessionEnv, "")
	run, err := Create("identity", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// liveProcess starts a process the test never ends, so a test can check that
// no signal reached it.
func liveProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// gonePID returns the PID of a process that has already ended.
func gonePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return pid
}

// A new run records the start time of its own process, so a later check can
// tell that process from one the kernel gives the same PID.
func TestCreateRecordsTheRunProcessStartTime(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	run := createRun(t, "wf_identity-create")
	started, err := procident.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if run.Meta.Pid != os.Getpid() || run.Meta.PidStart != started {
		t.Fatalf("run process = %d/%d, want %d/%d", run.Meta.Pid, run.Meta.PidStart, os.Getpid(), started)
	}
	if !Alive(run.Meta) {
		t.Fatal("the run's own process must count as alive")
	}
}

// A reused PID is another process, so it must be neither reported alive nor
// signalled.
func TestReusedPIDIsNotAliveAndIsNotSignalled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	run := createRun(t, "wf_identity-reused")
	other := liveProcess(t)
	otherStart, err := procident.StartTime(other.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	run.Meta.Pid, run.Meta.PidStart = other.Process.Pid, otherStart+1
	if err := run.saveMeta(); err != nil {
		t.Fatal(err)
	}
	if Alive(run.Meta) {
		t.Fatal("a PID whose start time changed must not count as alive")
	}
	if err := Cancel(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("cancel signalled the process that now holds the PID: %v", err)
	}
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Status != "canceled" {
		t.Fatalf("canceled run = %+v, %v; want status canceled", m, err)
	}
}

// The engine marks a run whose process is gone as failed at start, and leaves
// a run that still has its process alone.
func TestReconcileLostRunsMarksOnlyTheGoneOnes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	live := createRun(t, "wf_identity-live")
	lost := createRun(t, "wf_identity-lost")
	lost.Meta.Pid, lost.Meta.PidStart = gonePID(t), 1
	if err := lost.saveMeta(); err != nil {
		t.Fatal(err)
	}
	n, err := ReconcileLostRuns()
	if err != nil || n != 1 {
		t.Fatalf("ReconcileLostRuns = %d, %v; want 1 run failed", n, err)
	}
	m, err := ReadMeta(lost.Meta.ID)
	if err != nil || m.Status != "error" || m.Error != reconcileLostReason {
		t.Fatalf("lost run = %+v, %v; want status error with %q", m, err, reconcileLostReason)
	}
	if m, err := ReadMeta(live.Meta.ID); err != nil || m.Status != "running" {
		t.Fatalf("live run = %+v, %v; want it still running", m, err)
	}
}

// A run recorded before runs kept a start time cannot be verified, so it keeps
// the plain PID probe.
func TestRunWithoutAStartTimeKeepsThePIDProbe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	run := createRun(t, "wf_identity-legacy")
	run.Meta.PidStart = 0
	if err := run.saveMeta(); err != nil {
		t.Fatal(err)
	}
	if !Alive(run.Meta) {
		t.Fatal("a run of this process without a start time must count as alive")
	}
}
