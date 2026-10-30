package engine

import (
	"log"

	"dyna-agent/internal/profile"
)

// idleCompaction compacts a session that has sat idle for
// profile.IdleCompactAfter. Each turn end moves that session's one deadline on
// the engine's timer set, so nothing polls. When the deadline fires, compact
// decides whether the session is still idle and big enough.
type idleCompaction struct {
	timers *timers
	// turns returns the number of turns the session has run.
	turns func(id string) (int, error)
	// compact compacts session id if it is still idle after the given turn.
	compact func(id string, turn int) error
}

// turnEnded arms the deadline of each session whose turn just ended.
func (c idleCompaction) turnEnded(ids []string) {
	for _, id := range ids {
		turn, err := c.turns(id)
		if err != nil {
			continue // a session that is gone has nothing to compact
		}
		c.timers.at(timerIdleCompact+id, c.timers.now().Add(profile.IdleCompactAfter), func() {
			if err := c.compact(id, turn); err != nil {
				log.Printf("idle compaction of %s: %v", id, err)
			}
		})
	}
}
