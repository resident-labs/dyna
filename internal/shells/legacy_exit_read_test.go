package shells

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// A read shows an exit that only an older supervisor's exit.json recorded,
// without writing it into dyna.db. Sessions list their shells while they build
// instructions, so a read must not take the writer lock. The wait path keeps
// the exit once it sees it.
func TestStatusReadDoesNotWriteALegacyExit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shells")
	s := &Store{Root: root}
	t.Cleanup(func() { _ = database.Close(filepath.Dir(root)) })
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(root, meta.ID, "exit.json"), Exit{Code: 7, Ended: time.Now()}, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := s.Info(meta.ID)
	if err != nil || in.Status != "exited" || in.Exit == nil || in.Exit.Code != 7 {
		t.Fatalf("Info = %#v, %v", in, err)
	}
	if stored, err := s.ReadExit(meta.ID); err != nil || stored != nil {
		t.Fatalf("the read stored the legacy exit: %#v, %v", stored, err)
	}
	if in, done, err := s.Wait(context.Background(), meta.ID, time.Second, nil); err != nil || !done || in.Status != "exited" {
		t.Fatalf("Wait = %#v, %v, %v", in, done, err)
	}
	if err := os.Remove(filepath.Join(root, meta.ID, "exit.json")); err != nil {
		t.Fatal(err)
	}
	if stored, err := s.ReadExit(meta.ID); err != nil || stored == nil || stored.Code != 7 {
		t.Fatalf("the write path did not keep the legacy exit: %#v, %v", stored, err)
	}
}

// legacyExitJob creates a job whose exit only exit.json holds, and returns the
// record a waiter read before any write.
func legacyExitJob(t *testing.T) (*Store, Info) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shells")
	s := &Store{Root: root}
	t.Cleanup(func() { _ = database.Close(filepath.Dir(root)) })
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(root, meta.ID, "exit.json"), Exit{Code: 7, Ended: time.Now()}, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := s.Info(meta.ID)
	if err != nil || !in.legacyExit || in.Exit == nil {
		t.Fatalf("Info = %#v, %v", in, err)
	}
	return s, in
}

// A waiter that imports its earlier legacy snapshot must not overwrite an
// exit or metadata a supervisor committed in between. One conditional write
// sets only the exit columns, so the canonical record wins.
func TestLegacyExitImportKeepsANewerCanonicalExit(t *testing.T) {
	s, legacy := legacyExitJob(t)
	renamed := legacy.Meta
	renamed.Name = "supervisor"
	if err := s.Save(renamed, &Exit{Code: 9, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.importLegacyExit(legacy.Meta, legacy.Exit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Exit == nil || got.Exit.Code != 9 {
		t.Fatalf("canonical exit lost: %#v", got.Exit)
	}
	if got.Name != "supervisor" {
		t.Fatalf("metadata overwritten: %q", got.Name)
	}
}

// Two waiters that both saw the legacy exit leave one canonical exit.
func TestTwoLegacyExitWaitersKeepOneExit(t *testing.T) {
	s, legacy := legacyExitJob(t)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.importLegacyExit(legacy.Meta, legacy.Exit)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ex, err := s.ReadExit(legacy.ID); err != nil || ex == nil || ex.Code != 7 {
		t.Fatalf("stored exit = %#v, %v", ex, err)
	}
	if list, err := s.List(true); err != nil || len(list) != 1 {
		t.Fatalf("jobs = %#v, %v", list, err)
	}
}
