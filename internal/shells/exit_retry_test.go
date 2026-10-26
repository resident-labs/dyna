package shells

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-207: a state write that times out waiting for the writer must not lose a
// finished job's exit. Through Supervise, the exit is tried again until the
// store takes it, and the job reads as exited, not lost.
func TestSuperviseRecordsExitAfterStateStoreTimeouts(t *testing.T) {
	var pauses int
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), pause: func(time.Duration) { pauses++ }}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tries := 0
	s.exitWrite = func(m Meta, ex *Exit) error {
		if tries++; tries <= 2 {
			return context.DeadlineExceeded
		}
		return s.Save(m, ex)
	}
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "exited" || in.Exit == nil || in.Exit.Code != 0 {
		t.Fatalf("job = %#v, %v; want exited with code 0", in, err)
	}
	if tries != 3 || pauses != 2 {
		t.Fatalf("tries = %d, pauses = %d; want 3 and 2", tries, pauses)
	}
}

// A store that never answers ends Supervise with a state-store timeout that
// carries the command's exit, never a silent end.
func TestSuperviseReportsStateStoreTimeout(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells"), pause: func(time.Duration) {}}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.exitWrite = func(Meta, *Exit) error { return context.DeadlineExceeded }
	err = s.Supervise(context.Background(), meta.ID)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "state store timed out") || !strings.Contains(err.Error(), "code 0") {
		t.Fatalf("error = %v", err)
	}
}

// The driver may report the timeout as plain text; any other error is final.
func TestSaveExitTimeoutText(t *testing.T) {
	if !timedOut(errors.New("Error: context deadline exceeded")) || timedOut(errors.New("disk full")) || timedOut(nil) {
		t.Fatal("timedOut misjudged an error")
	}
}
