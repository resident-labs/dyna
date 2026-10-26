//go:build linux

package runsvc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runprocs"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
)

// D-185: a worker in a process group of its own does not outlive a canceled run.
func TestCancelEndsTheRunsWorkerProcessGroup(t *testing.T) {
	dataDir := storetest.Isolate(t)
	runID := "wf_20261026-010101-abcdef02"
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "cancel-groups", Status: "running", StartedAt: time.Now()})
	worker := exec.Command("sleep", "600")
	worker.Env = append(os.Environ(), "DYNA_WORKER_RUN="+runID)
	worker.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	if err := Cancel(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), runID); err != nil {
		t.Fatal(err)
	}
	// Cancel waits for the group to exit, so the worker is already a zombie.
	var status syscall.WaitStatus
	if pid, err := syscall.Wait4(worker.Process.Pid, &status, syscall.WNOHANG, nil); err != nil || pid == 0 || status.Signal() != syscall.SIGKILL {
		t.Fatalf("worker was not killed by cancel: pid=%d status=%v err=%v", pid, status, err)
	}
}

// A coordinator that ignores SIGTERM is killed after the grace period, and
// the run is then finalized as canceled instead of staying running.
func TestCancelFinalizesARunWhoseCoordinatorNeededForcedTermination(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func(old time.Duration) func() { return func() { runExitGrace = old } }(runExitGrace))
	runExitGrace = 0
	runID := "wf_20261026-010101-abcdef03"
	coordinator := exec.Command("sh", "-c", `trap "" TERM; echo ready; exec sleep 600`)
	coordinator.Env = append(os.Environ(), "DYNA_WORKER_RUN="+runID)
	coordinator.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := coordinator.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.Process.Kill(); _ = coordinator.Wait() })
	// The trap is set once the shell has printed; exec keeps the ignored signal.
	if _, err := out.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "forced", Status: "running", Pid: coordinator.Process.Pid, StartedAt: time.Now()})
	if err := Cancel(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), runID); err != nil {
		t.Fatal(err)
	}
	if m, err := runstore.ReadMeta(runID); err != nil || m.Status != "canceled" {
		t.Fatalf("run = %s, error=%v, want canceled", m.Status, err)
	}
}

// A kept shell job outlives the run, though its processes carry the run's stamp.
func TestCancelLeavesAKeptWorkerShellRunning(t *testing.T) {
	dataDir := storetest.Isolate(t)
	runID := "wf_20261026-010101-abcdef04"
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "keep", Status: "running", StartedAt: time.Now()})
	t.Setenv("DYNA_WORKER_RUN", runID)
	s := shells.DefaultStore()
	meta, err := s.Create(shells.StartOptions{Run: runID, Keep: true, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 1)
	s.OnStarted = func(pid int) { started <- pid }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Supervise(ctx, meta.ID) }()
	pid := <-started
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		cancel()
		<-done
	})
	if err := Cancel(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), runID); err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "running" {
		t.Fatalf("kept shell = %s, error=%v, want running", in.Status, err)
	}
}

// A kept job's child in a group of its own is part of the job and survives too.
func TestCancelLeavesAKeptShellsDetachedChildRunning(t *testing.T) {
	dataDir := storetest.Isolate(t)
	runID := "wf_20261026-010101-abcdef05"
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "keep-child", Status: "running", StartedAt: time.Now()})
	t.Setenv("DYNA_WORKER_RUN", runID)
	fifo := filepath.Join(t.TempDir(), "up")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	s := shells.DefaultStore()
	meta, err := s.Create(shells.StartOptions{Run: runID, Keep: true, Cwd: t.TempDir(),
		Cmd: []string{"sh", "-c", `setsid sh -c "sleep 600 & echo up > ` + fifo + `; wait" & wait`}})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 1)
	s.OnStarted = func(pid int) { started <- pid }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Supervise(ctx, meta.ID) }()
	pid := <-started
	var jobPids []int
	t.Cleanup(func() {
		for _, p := range jobPids {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		cancel()
		<-done
	})
	if _, err := os.ReadFile(fifo); err != nil { // returns once the child is up; it does not exec again, so its environment stays readable
		t.Fatal(err)
	}
	jobPids = runprocs.Stamped(shells.JobEnv + "=" + meta.ID)
	if len(jobPids) < 2 {
		t.Fatalf("job processes = %v, want the shell and its child", jobPids)
	}
	// The child signals after setsid, so it is already outside the job's group.
	detached := false
	for _, p := range jobPids {
		if g, err := syscall.Getpgid(p); err == nil && g != pid {
			detached = true
		}
	}
	if !detached {
		t.Fatalf("no job process left the job's group %d: %v", pid, jobPids)
	}
	if err := Cancel(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), runID); err != nil {
		t.Fatal(err)
	}
	for _, p := range jobPids {
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p)); err != nil || strings.Contains(string(stat), ") Z ") {
			t.Fatalf("kept job process %d ended: %v", p, err)
		}
	}
}
