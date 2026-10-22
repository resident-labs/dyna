package goals

import (
	"fmt"
	"time"
)

// Wait records an expected delay without treating it as progress.
func (s *Store) Wait(room, id, by string, until time.Time) (Goal, error) {
	var out Goal
	where, args := named(room, id)
	err := s.update(func(st *State) error {
		r := st.Rooms[room]
		if r == nil || r.Goals[id] == nil {
			return fmt.Errorf("goal %s does not exist in this room", id)
		}
		g := r.Goals[id]
		if !g.Open() {
			return fmt.Errorf("edit this goal to reopen it before waiting")
		}
		now := s.now()
		if !until.After(now) {
			return fmt.Errorf("wait deadline must be in the future")
		}
		g.WaitAt, g.WaitUntil = now, until
		g.Edits = append(g.Edits, Edit{At: now, By: by, OldText: g.Text, WaitUntil: until})
		out = *g
		return nil
	}, where, args...)
	return out, err
}
