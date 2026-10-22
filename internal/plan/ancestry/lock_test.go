package ancestry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestTwoAncestryReadersImportForeignHeaderOnce(t *testing.T) {
	s := testLockedStore(t)
	dir := filepath.Join(s.DataDir, "runs", "wf_race")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(`{"resumedFrom":"wf_parent"}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE import_count(n INTEGER); INSERT INTO import_count VALUES(0); CREATE TRIGGER count_plan_import AFTER INSERT ON plan_ancestry BEGIN UPDATE import_count SET n=n+1; END"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := s.lock(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	ready := make(chan struct{}, 2)
	oldBefore := beforePlanLock
	beforePlanLock = func() { ready <- struct{}{} }
	defer func() { beforePlanLock = oldBefore }()
	type result struct {
		parent string
		err    error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { parent, err := s.Parent(ctx, dir); done <- result{parent, err} }()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("readers did not reach the lock", ctx.Err())
		}
	}
	unlock()
	unlock = nil
	for range 2 {
		var got result
		select {
		case got = <-done:
		case <-ctx.Done():
			t.Fatal("readers did not finish", ctx.Err())
		}
		if got.err != nil || got.parent != "wf_parent" {
			t.Fatalf("reader = %q,%v", got.parent, got.err)
		}
	}
	var imports int
	if err := db.QueryRow("SELECT n FROM import_count").Scan(&imports); err != nil || imports != 1 {
		t.Fatalf("imports = %d,%v", imports, err)
	}
}

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
