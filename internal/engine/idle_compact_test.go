package engine

import (
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

// A turn end arms one deadline per session, IdleCompactAfter later. The next
// turn end moves it, so a session that keeps working is never compacted, and a
// session that is gone arms nothing.
func TestIdleCompactionFiresOnceAfterTheLastTurnEnd(t *testing.T) {
	clock := newFakeClock()
	type call struct {
		id   string
		turn int
	}
	var calls []call
	turns := map[string]int{"a": 1, "b": 4}
	c := idleCompaction{
		timers: clock.timers(t.Context()),
		turns: func(id string) (int, error) {
			n, ok := turns[id]
			if !ok {
				return 0, errors.New("gone")
			}
			return n, nil
		},
		compact: func(id string, turn int) error {
			calls = append(calls, call{id, turn})
			return nil
		},
	}
	c.turnEnded([]string{"a", "b", "missing"})
	clock.advance(profile.IdleCompactAfter - time.Minute)
	turns["a"] = 2
	c.turnEnded([]string{"a"}) // a ran another turn: its deadline moves
	clock.advance(time.Minute)
	if len(calls) != 1 || calls[0] != (call{"b", 4}) {
		t.Fatalf("calls after the first deadline = %v, want only b at turn 4", calls)
	}
	clock.advance(profile.IdleCompactAfter)
	if len(calls) != 2 || calls[1] != (call{"a", 2}) {
		t.Fatalf("calls after the moved deadline = %v, want a at turn 2", calls)
	}
}

// At start the engine arms each idle session from its last turn end: a deadline
// already past fires at once, and one still ahead fires when it is due.
func TestIdleCompactionRestoreArmsFromTheLastTurnEnd(t *testing.T) {
	clock := newFakeClock()
	now := clock.Now()
	var compacted []string
	c := idleCompaction{
		timers: clock.timers(t.Context()),
		idle: func() ([]idleSession, error) {
			return []idleSession{
				{"old", 3, now.Add(-time.Hour)},
				{"recent", 5, now.Add(-profile.IdleCompactAfter + 10*time.Minute)},
			}, nil
		},
		compact: func(id string, turn int) error {
			compacted = append(compacted, id)
			return nil
		},
	}
	if err := c.restore(); err != nil {
		t.Fatal(err)
	}
	clock.advance(0)
	if len(compacted) != 1 || compacted[0] != "old" {
		t.Fatalf("compacted at start = %v, want only old", compacted)
	}
	clock.advance(10 * time.Minute)
	if len(compacted) != 2 || compacted[1] != "recent" {
		t.Fatalf("compacted after 10 minutes = %v, want recent next", compacted)
	}
}
