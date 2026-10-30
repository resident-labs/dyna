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
