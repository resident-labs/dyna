package goals

import "fmt"

// InheritedGoal returns the goal a session's work serves: its own effective
// goal, else the nearest ancestor's. parent maps a session to its parent, or
// "" at the top. It returns "" when no goal is found or a session on the way
// is ambiguous, so a caller never links a guess.
func (s *Store) InheritedGoal(room, session string, parent func(string) string) string {
	for depth := 0; session != "" && depth < 16; depth++ {
		id, err := s.EffectiveGoal(room, session, "")
		if err != nil {
			return ""
		}
		if id != "" {
			return id
		}
		session = parent(session)
	}
	return ""
}

// CheckLinkable reports why a change cannot link to goal id of room.
func (s *Store) CheckLinkable(room, id string) error {
	g, err := s.Get(room, id)
	if err != nil {
		return err
	}
	if !g.Open() {
		return fmt.Errorf("goal %s is not open", id)
	}
	return nil
}
