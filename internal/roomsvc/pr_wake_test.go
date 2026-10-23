package roomsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/store/storetest"
)

// A PR wake scans PRs at once, without waiting for the next tick, and a wake
// before Watch waits is kept.
func TestPRWakeScansWithoutATick(t *testing.T) {
	storetest.Isolate(t)
	s := New(nil)
	s.PRsChanged()
	s.PRsChanged() // one pending wake is enough
	ctx, cancel := context.WithCancel(context.Background())
	scans := 0
	ticked := s.waitTick(ctx, nil, func() { scans++; cancel() }, func() { t.Fatal("a PR wake scanned sessions") }, func() { t.Fatal("a PR wake scanned coordinators") }, func() { t.Fatal("a PR wake scanned call requests") })
	if ticked || scans != 1 {
		t.Fatalf("waitTick returned %v after %d scans; want one scan, then false when ctx ends", ticked, scans)
	}
}

// Session wakes coalesce: a burst of writes before Watch waits gives one
// scan, which takes every named session.
func TestSessionWakesCoalesce(t *testing.T) {
	storetest.Isolate(t)
	s := New(nil)
	for _, id := range []string{"ses_a", "ses_b", "ses_c", "ses_a"} {
		s.SessionChanged(id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	scans, taken := 0, 0
	ticked := s.waitTick(ctx, nil, func() { t.Fatal("a session wake scanned PRs") }, func() {
		scans++
		ids, all := s.takeDirty()
		taken = len(ids)
		if all {
			taken = -1
		}
		cancel()
	}, func() { t.Fatal("a session wake scanned coordinators") }, func() { t.Fatal("a session wake scanned call requests") })
	if ticked || scans != 1 || taken != 3 {
		t.Fatalf("waitTick returned %v after %d scans of %d sessions; want one scan of 3", ticked, scans, taken)
	}
}
