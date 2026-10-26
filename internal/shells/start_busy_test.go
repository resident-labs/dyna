package shells

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-220: when the state store is too busy to record the job and its hold, the
// job must not be left behind as a record that later reads as lost, and
// nothing may start.
func TestStartJobLeavesNothingWhenStateStoreIsBusy(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	s.failWrite = func(op string) error {
		if op == "save" {
			return context.DeadlineExceeded
		}
		return nil
	}
	_, err := s.StartJob(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()}, time.Now(), func(Meta) error {
		t.Error("the supervisor started although the job could not be recorded")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "job did not start: the state store was busy; retry") {
		t.Fatalf("error = %v; want the busy message", err)
	}
	if list, lerr := s.List(true); lerr != nil || len(list) != 0 {
		t.Fatalf("jobs left behind = %v, %v", list, lerr)
	}
	if left, _ := filepath.Glob(filepath.Join(s.Root, idPrefix+"*")); len(left) != 0 {
		t.Fatalf("job folders left behind: %v", left)
	}
}

// If the cleanup delete times out too, the row may stay, so its folder stays and
// the error says the job could not be removed.
func TestStartJobKeepsFolderWhenItsRowCannotBeDeleted(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), pause: func(time.Duration) {}}
	s.failWrite = func(op string) error {
		if strings.Contains(op, "DELETE") {
			return context.DeadlineExceeded
		}
		return nil
	}
	meta, err := s.StartJob(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()}, time.Now(), func(Meta) error { return errors.New("no supervisor") })
	if err == nil || !strings.Contains(err.Error(), "could not be removed") || !strings.Contains(err.Error(), "job did not start") {
		t.Fatalf("error = %v; want did not start and could not be removed", err)
	}
	if _, serr := os.Stat(filepath.Join(s.Root, meta.ID)); serr != nil {
		t.Fatalf("the folder of a job whose row stays must stay: %v", serr)
	}
}

// A failed spawn also removes the job.
func TestStartJobRemovesJobWhenSpawnFails(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	boom := errors.New("no such executable")
	meta, err := s.StartJob(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()}, time.Now(), func(Meta) error { return boom })
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "job did not start") || meta.ID == "" {
		t.Fatalf("meta = %v, error = %v", meta, err)
	}
	if list, lerr := s.List(true); lerr != nil || len(list) != 0 {
		t.Fatalf("jobs left behind = %v, %v", list, lerr)
	}
}

// The supervisor's first write times out: it tries again, and if the store
// stays busy it ends with the busy message, which a lost job report shows.
func TestSuperviseReportsBusyStateStoreAtStart(t *testing.T) {
	var pauses int
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), pause: func(time.Duration) { pauses++ }}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.failWrite = func(op string) error {
		if op == "save" {
			return context.DeadlineExceeded
		}
		return nil
	}
	err = s.Supervise(context.Background(), meta.ID)
	if err == nil || !strings.Contains(err.Error(), "job did not start: the state store was busy; retry") {
		t.Fatalf("error = %v; want the busy message", err)
	}
	if pauses != startSaveTries-1 {
		t.Fatalf("pauses = %d; want %d", pauses, startSaveTries-1)
	}
}
