package ancestry

import (
	"context"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestAncestryLockWaitHonorsCancellation(t *testing.T) {
	s := testLockedStore(t)
	dir := filepath.Join(s.DataDir, "runs", "wf_cancel_lock")
	unlock, err := s.lock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ready := make(chan struct{}, 1)
	oldBefore := beforePlanLock
	beforePlanLock = func() { ready <- struct{}{} }
	defer func() { beforePlanLock = oldBefore }()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		release, err := s.lock(ctx, dir)
		if release != nil {
			release()
		}
		done <- err
	}()
	<-ready
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("lock wait = %v", err)
	}
}

func testLockedStore(t *testing.T) Store {
	t.Helper()
	dataDir := storetest.DataDir(t)
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	return Store{DataDir: dataDir}
}
