package shells

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// D-207: a state write that times out waiting for the writer must not lose a
// finished job's exit. The supervisor tries again until the store takes it.
func TestSaveExitRetriesWhileStateStoreTimesOut(t *testing.T) {
	var paused []time.Duration
	s := &Store{pause: func(d time.Duration) { paused = append(paused, d) }}
	calls := 0
	err := s.saveExit(func() error {
		calls++
		if calls < 3 {
			return context.DeadlineExceeded
		}
		return nil
	}, Exit{Code: 7})
	if err != nil || calls != 3 || len(paused) != 2 {
		t.Fatalf("err = %v, calls = %d, pauses = %d; want a saved exit after 3 tries", err, calls, len(paused))
	}
}

// The driver may report the timeout as plain text.
func TestSaveExitRetriesFlattenedTimeout(t *testing.T) {
	s := &Store{pause: func(time.Duration) {}}
	calls := 0
	err := s.saveExit(func() error {
		calls++
		if calls == 1 {
			return errors.New("Error: context deadline exceeded")
		}
		return nil
	}, Exit{})
	if err != nil || calls != 2 {
		t.Fatalf("err = %v, calls = %d; want a saved exit after 2 tries", err, calls)
	}
}

// A store that never answers ends as a state-store timeout that carries the
// command's exit, never as a missing supervisor.
func TestSaveExitReportsStateStoreTimeout(t *testing.T) {
	s := &Store{pause: func(time.Duration) {}}
	err := s.saveExit(func() error { return context.DeadlineExceeded }, Exit{Code: 7})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "state store timed out") || !strings.Contains(err.Error(), "code 7") {
		t.Fatalf("error = %v", err)
	}
}

// Any other write error is final at once.
func TestSaveExitDoesNotRetryOtherErrors(t *testing.T) {
	s := &Store{pause: func(time.Duration) { t.Fatal("paused for an error that is not a timeout") }}
	boom := errors.New("disk full")
	if err := s.saveExit(func() error { return boom }, Exit{}); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}
