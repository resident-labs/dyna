package runsvc

import (
	"context"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
)

// D-165: cancelling a run stops the managed shell jobs its workers started.
// A canceled workflow's workers are gone, so without this their shell jobs
// keep running until the owning session closes.
func TestCancelStopsTheRunsWorkerShells(t *testing.T) {
	dataDir := storetest.Isolate(t)
	runID := "wf_20261026-010101-abcdef01"
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "cancel-shells", Status: "running", StartedAt: time.Now()})
	s := shells.DefaultStore()
	meta, err := s.Create(shells.StartOptions{Run: runID, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 1)
	s.OnStarted = func(pid int) { started <- pid }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Supervise(ctx, meta.ID) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("supervisor: %v", err)
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not start")
	}
	// A run without a live process finalizes as canceled instead of signalling.
	if err := Cancel(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), runID); err != nil {
		t.Fatal(err)
	}
	in, err := s.Info(meta.ID)
	if err != nil || in.Status != "exited" {
		t.Fatalf("worker shell = %s, want exited, error=%v", in.Status, err)
	}
	if in.Pid > 0 && syscall.Kill(in.Pid, 0) == nil {
		t.Fatal("worker shell process still exists after cancel")
	}
}
