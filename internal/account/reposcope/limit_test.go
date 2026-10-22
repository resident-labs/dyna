package reposcope

import (
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// A shorter limit must never shorten a longer known limit, even when two
// callers record limits for the same account at once.
func TestMarkLimitedKeepsTheLongestLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	now := time.Unix(1_700_000_000, 0)
	hour := now.Add(time.Hour)
	if err := s.MarkLimited("acct", hour); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLimited("acct", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertLimit(t, s, now, "acct", hour)
	twoHours := now.Add(2 * time.Hour)
	if err := s.MarkLimited("acct", twoHours); err != nil {
		t.Fatal(err)
	}
	assertLimit(t, s, now, "acct", twoHours)
}

func assertLimit(t *testing.T, s *Store, now time.Time, accountID string, want time.Time) {
	t.Helper()
	limits, err := s.Limits(now)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := limits[accountID]
	if !ok {
		t.Fatalf("Limits has no limit for %s, want one until %d", accountID, want.Unix())
	}
	if got.Unix() != want.Unix() {
		t.Fatalf("Limits[%s] until %d, want %d", accountID, got.Unix(), want.Unix())
	}
}
