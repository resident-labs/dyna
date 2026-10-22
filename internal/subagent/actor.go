package subagent

import (
	"fmt"
	"os"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
)

// Actor returns who a Dyna CLI command acts as. Identity comes only from the
// Dyna stamp (mode.CurrentSession); no flag selects another session. A
// stamped session acts as itself, read through resolve. No stamp means root,
// the room owner: the Info has no ID and carries the room from
// room.ResolveContext (roomRef, the room stamp, or the current repo room) and
// the working directory. Commands that only need the acting session ID use
// mode.CurrentSession directly; "" is root.
func Actor(rooms *room.Store, resolve func(string) (Info, error), roomRef string) (Info, error) {
	if cur, ok := mode.CurrentSession(); ok {
		info, err := resolve(cur.ID)
		if err != nil {
			return Info{}, err
		}
		if roomRef != "" {
			want, err := rooms.Resolve(roomRef)
			if err != nil {
				return Info{}, err
			}
			if want.ID != info.RoomID {
				return Info{}, fmt.Errorf("session %s acts in room %q and cannot act in %q", info.ID, info.Room, want.Name)
			}
		}
		return info, nil
	}
	rec, err := rooms.ResolveContext(roomRef, false)
	if err != nil {
		return Info{}, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return Info{}, err
	}
	return Info{Meta: Meta{RoomID: rec.ID, Room: rec.Name, Dir: dir}}, nil
}

// Target resolves a session that a CLI command changes, such as stop, close,
// finish. A stamped session may act only on itself and its
// descendants. Root, with no stamp, may act on any session. Dyna's own
// services call the Manager directly and are not limited.
func (m *Manager) Target(ref string) (Info, error) {
	info, err := m.store().Resolve(ref)
	if err != nil {
		return Info{}, err
	}
	cur, ok := mode.CurrentSession()
	if !ok {
		return info, nil
	}
	return info, m.CheckActsOn(cur.ID, info)
}

// CheckActsOn refuses a session acting on a session that is neither itself
// nor one of its descendants. The engine applies it to a session caller of
// a session control; the CLI's Target applies it to its own session.
func (m *Manager) CheckActsOn(session string, info Info) error {
	s := m.store()
	seen := map[string]bool{}
	for id := info.ID; id != "" && !seen[id]; {
		if id == session {
			return nil
		}
		seen[id] = true
		meta, err := s.ReadMeta(id)
		if err != nil {
			break
		}
		id = meta.ParentSession
	}
	return fmt.Errorf("session %s may act only on itself and its descendants, not on %s", session, info.Address())
}
