package engine

import (
	"log"
	"time"

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
	// idle lists the open sessions that are idle and big enough to compact.
	idle func() ([]idleSession, error)
	// compact compacts session id if it is still idle after the given turn.
	compact func(id string, turn int) error
}

// idleSession is an idle session as the store last saw it.
type idleSession struct {
	id    string
	turn  int
	ended time.Time // when its last turn ended
}

// restore arms the deadline of each idle session from its last turn end, which
// the store keeps, so an engine restart loses none. A deadline already past
// fires at once; compact applies the same skips as for any deadline.
func (c idleCompaction) restore() error {
	sessions, err := c.idle()
	for _, s := range sessions {
		c.arm(s.id, s.turn, s.ended.Add(profile.IdleCompactAfter))
	}
	return err
}

// arm sets the deadline of session id.
func (c idleCompaction) arm(id string, turn int, when time.Time) {
	c.timers.at(timerIdleCompact+id, when, func() {
		if err := c.compact(id, turn); err != nil {
			log.Printf("idle compaction of %s: %v", id, err)
		}
	})
}

// turnEnded arms the deadline of each session whose turn just ended.
func (c idleCompaction) turnEnded(ids []string) {
	for _, id := range ids {
		turn, err := c.turns(id)
		if err != nil {
			continue // a session that is gone has nothing to compact
		}
		c.arm(id, turn, c.timers.now().Add(profile.IdleCompactAfter))
	}
}
