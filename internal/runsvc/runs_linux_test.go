//go:build linux

package runsvc

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
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
