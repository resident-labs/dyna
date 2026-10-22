package goals

import "fmt"

// HandoffHolder replaces one holder only while the goal is still open and
// held by from. The ownership change and the heir's notice commit together.
func (s *Store) HandoffHolder(room, id, from, to string) (moved bool, err error) {
	where, args := named(room, id)
	err = s.updateTx(func(w *wake, st *State) error {
		moved = false // updateTx may retry after taking the heir's session lock.
		r := st.Rooms[room]
		if r == nil {
			return nil
		}
		g := r.Goals[id]
		if g == nil || !g.Open() || !g.HeldBy(from) || from == to {
			return nil
		}
		if to == "" {
			return fmt.Errorf("goal %s has no open ancestor or root", id)
		}
		if err := w.activate(to); err != nil {
			return fmt.Errorf("hand goal %s to %s: %w", id, to, err)
		}
		for i, holder := range g.Holders {
			if holder == from {
				g.Holders[i] = to
			}
		}
		g.Holders = unique(g.Holders)
		now := s.now()
		g.UpdatedAt = now
		r.Notices = append(r.Notices, Notice{
			Kind: "holder_closed", ID: fmt.Sprintf("goal_%s_handoff_%d", id, now.UnixNano()),
			Room: room, Goal: id, To: to,
			Text: fmt.Sprintf("%s moved to you because session %s is closing. Continue or reassign this work. Inspect: dyna goal show %s. Source context: dyna subagent output %s.", Clip(g.Text, 200), from, id, from),
		})
		moved = true
		return nil
	}, where, args...)
	return moved && err == nil, err
}
