package goals

import (
	"fmt"
	"time"
)

// ExpediteTTL is how long a goal stays expedited if nobody stops it first.
const ExpediteTTL = 24 * time.Hour

// ExpeditedAt reports whether the goal is open and expedited at now.
func (g Goal) ExpeditedAt(now time.Time) bool {
	return g.Open() && g.ExpediteUntil != nil && g.ExpediteUntil.After(now)
}

// dropExpired clears an expired expedite time, so readers never see one.
func (g *Goal) dropExpired(now time.Time) {
	if g.ExpediteUntil != nil && !g.ExpediteUntil.After(now) {
		g.ExpediteUntil = nil
	}
}

func (s *Store) dropExpired(st *State) {
	now := s.now()
	for _, r := range st.Rooms {
		for _, g := range r.Goals {
			g.dropExpired(now)
		}
	}
}

// Expedited returns the room's expedited goal, or nil if it has none.
func (s *Store) Expedited(room string) (*Goal, error) {
	open, err := s.ListOpen(room)
	if err != nil {
		return nil, err
	}
	for _, g := range open {
		if g.ExpeditedAt(s.now()) {
			return &g, nil
		}
	}
	return nil, nil
}

// IsExpedited reports whether goalID is the room's expedited goal.
func (s *Store) IsExpedited(room, goalID string) bool {
	g, err := s.Expedited(room)
	return err == nil && g != nil && g.ID == goalID
}

// Expedite makes id the room's one expedited goal for ExpediteTTL. Expediting
// the goal that already holds it changes nothing. Another goal holds it only
// until replace is true. One transaction clears the old goal and marks the new.
func (s *Store) Expedite(room, id string, replace bool) (Goal, error) {
	var out Goal
	err := s.update(func(st *State) error {
		r := st.Room(room)
		g := r.Goals[id]
		if g == nil || !g.Open() {
			if g == nil {
				return fmt.Errorf("goal %s does not exist in this room", id)
			}
			return fmt.Errorf("goal %s is not open", id)
		}
		now := s.now()
		for _, other := range r.Goals {
			if other.ID == id || !other.ExpeditedAt(now) {
				continue
			}
			if !replace {
				return fmt.Errorf("goal %s is already expedited in this room; use replace to switch", other.ID)
			}
			other.ExpediteUntil = nil
		}
		if !g.ExpeditedAt(now) {
			until := now.Add(ExpediteTTL)
			g.ExpediteUntil = &until
		}
		out = *g
		return nil
	}, openGoals)
	return out, err
}

// StopExpedite clears the room's expedited goal. It returns that goal, or the
// zero Goal if the room had none.
func (s *Store) StopExpedite(room string) (Goal, error) {
	var out Goal
	err := s.update(func(st *State) error {
		r := st.Rooms[room]
		if r == nil {
			return nil
		}
		for _, g := range r.Goals {
			if g.ExpediteUntil == nil {
				continue
			}
			if g.ExpeditedAt(s.now()) {
				out = *g
				out.ExpediteUntil = nil
			}
			g.ExpediteUntil = nil
		}
		return nil
	}, openGoals)
	return out, err
}
