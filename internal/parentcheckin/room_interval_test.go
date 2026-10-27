package parentcheckin

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// A room's own check-in setting decides when its parents are checked: an
// override moves the deadline, off delivers nothing, and a room without a
// setting keeps the global interval.
func TestRoomIntervalOverridesScheduleAndOffStopsCheckIns(t *testing.T) {
	root := t.TempDir()
	if err := Migrate(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var delivered []string
	s := &Scheduler{
		Root: root,
		Now:  func() time.Time { return now },
		Interval: func() (time.Duration, error) {
			return 15 * time.Minute, nil
		},
		IntervalFor: func(roomID string, global time.Duration) time.Duration {
			switch roomID {
			case "fast":
				return 5 * time.Minute
			case "quiet":
				return 0
			}
			return global
		},
		Targets: func() ([]Target, error) {
			return []Target{{ID: "p-fast", Room: "fast"}, {ID: "p-quiet", Room: "quiet"}, {ID: "p-default", Room: "plain"}}, nil
		},
		Deliver: func(_ context.Context, target Target, _ string) (bool, error) {
			delivered = append(delivered, target.ID)
			return true, nil
		},
	}
	count := func(id string) int {
		t.Helper()
		n := 0
		for _, got := range delivered {
			if got == id {
				n++
			}
		}
		return n
	}
	tick := func() time.Time {
		t.Helper()
		next, err := s.Tick(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	// The shortest room interval owns the next deadline.
	if next := tick(); !next.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("first deadline %v, want the overridden 5m", next)
	}
	// The 5m room delivers at 5m; the 15m rooms have not fired yet.
	now = now.Add(5 * time.Minute)
	tick()
	if count("p-fast") != 1 || count("p-default") != 0 {
		t.Fatalf("at 5m: fast %d, default %d", count("p-fast"), count("p-default"))
	}
	// At 10m only the 5m room has fired again.
	now = now.Add(5 * time.Minute)
	tick()
	if count("p-fast") != 2 || count("p-default") != 0 {
		t.Fatalf("at 10m: fast %d, default %d", count("p-fast"), count("p-default"))
	}
	// At 15m the room without a setting fires once; the off room never fires.
	now = now.Add(5 * time.Minute)
	tick()
	if count("p-default") != 1 {
		t.Fatalf("at 15m: default %d", count("p-default"))
	}
	if got := count("p-quiet"); got != 0 {
		t.Fatalf("the off room received %d check-ins", got)
	}
}
