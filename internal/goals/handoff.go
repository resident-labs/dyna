package goals

import (
	"fmt"
	"slices"
)

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

// ReleaseHolder hands a goal to no session: it stays open and unheld, and the
// owner takes it, reassigns it, or closes it. Root never holds goals, so a
// close that would hand work to root releases the goal instead.
func (s *Store) ReleaseHolder(room, id, from string) (released bool, err error) {
	where, args := named(room, id)
	err = s.updateTx(func(_ *wake, st *State) error {
		released = false
		r := st.Rooms[room]
		if r == nil {
			return nil
		}
		g := r.Goals[id]
		if g == nil || !g.Open() || !g.HeldBy(from) {
			return nil
		}
		g.Holders = slices.DeleteFunc(g.Holders, func(h string) bool { return h == from })
		if g.Setter == from {
			g.Setter = "owner"
		}
		now := s.now()
		g.UpdatedAt = now
		r.Notices = append(r.Notices, Notice{
			Kind: "holder_closed", ID: fmt.Sprintf("goal_%s_released_%d", id, now.UnixNano()),
			Room: room, Goal: id,
			Text: fmt.Sprintf("Session %s closed, so goal %s lost its holder. Take it, reassign it, or close it. Inspect: dyna goal show %s. Source context: dyna subagent output %s.", from, id, id, from),
		})
		released = true
		return nil
	}, where, args...)
	return released && err == nil, err
}
