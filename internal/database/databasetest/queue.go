// Package databasetest provides shared helpers for database writer tests.
package databasetest

import (
	"runtime"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// WaitForWriter waits until more than before writers are queued. Tests
// hold the writer lock and start a write or lifecycle barrier first.
// finished reports that operation returning; a return before it reaches
// the queue is a failure. The deadline bounds a broken test, not the wait.
func WaitForWriter(t *testing.T, before int64, finished <-chan error) {
	t.Helper()
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	for database.Waiting() <= before {
		select {
		case err := <-finished:
			t.Fatalf("operation returned before reaching the writer queue: %v", err)
		case <-deadline.C:
			t.Fatal("operation did not reach the writer queue")
		case <-t.Context().Done():
			t.Fatalf("waiting for writer queue: %v", t.Context().Err())
		default:
			runtime.Gosched()
		}
	}
}
