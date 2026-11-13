// Package testutil contains synchronization helpers shared by tests.
package testutil

import (
	"testing"
	"time"
)

// Eventually waits for an observable condition, bounded by a deadline. Prefer
// channels when the fixture owns a completion signal; polling is for boundaries
// such as subprocess files or state owned by a component without a notification.
func Eventually(t testing.TB, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}
