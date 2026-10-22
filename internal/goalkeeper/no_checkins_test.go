package goalkeeper

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

// No check-in wake survives: no "how close are you" post, no goalless
// reminder, and the journal gains no entry.
func TestCheckInWakesAreGone(t *testing.T) {
	e := newCheckInEnv(t)
	e.now = e.now.Add(8 * time.Hour)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, m := range e.posts(room.GoalKeeperSender) {
		if strings.Contains(m.Text, "how close are you") {
			t.Fatalf("a check-in wake survived: %+v", m)
		}
		if strings.HasPrefix(m.Text, "Your session has had no goal") {
			t.Fatalf("a goalless reminder survived: %+v", m)
		}
	}
	if entries, _, err := e.s.Store.Journal(e.rec.ID, "", 0); err != nil || len(entries) != 0 {
		t.Fatalf("the labeling pass wrote journal entries: %v err=%v", entries, err)
	}
}

// The stale nudge is the only wake a holder can get: once per 6-hour window,
// only to the holder, and never to root.
func TestStaleNudgeIsTheOnlyHolderWake(t *testing.T) {
	e := newCheckInEnv(t)
	e.now = e.now.Add(goals.StaleAfter + time.Hour)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	nudges := 0
	for _, m := range e.posts(room.GoalKeeperSender) {
		if !strings.Contains(m.Text, "is stale") {
			continue
		}
		nudges++
		for _, to := range m.To {
			allowed := strings.HasPrefix(to, "work:lead") || strings.HasPrefix(to, "work:busy") || strings.HasPrefix(to, "work:child")
			if !allowed {
				t.Fatalf("a stale nudge went to a non-holder: %+v", m)
			}
		}
	}
	if nudges == 0 {
		t.Fatal("no stale nudge after the window")
	}
	// Inside the window, a second tick nudges nobody new.
	before := nudges
	e.now = e.now.Add(time.Hour)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range e.posts(room.GoalKeeperSender) {
		if strings.Contains(m.Text, "is stale") {
			count++
		}
	}
	if count != before {
		t.Fatalf("nudges inside the window: %d then %d", before, count)
	}
}
