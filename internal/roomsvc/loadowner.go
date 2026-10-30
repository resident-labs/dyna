package roomsvc

import (
	"strings"

	"dyna-agent/internal/runstore"
)

// LoadOwner resolves a session or run ID. An unknown ID is shown as itself.
func (s *Service) LoadOwner(id string) LoadOwner {
	out := LoadOwner{ID: id, Name: id}
	if strings.HasPrefix(id, "wf_") {
		m, err := runstore.ReadMeta(id)
		if err != nil {
			return out
		}
		if m.Name != "" {
			out.Name = m.Name
		}
		if rec, err := s.rooms().Resolve(s.runRoom(m)); err == nil {
			out.Room, out.RoomID = rec.Name, rec.ID
		}
		return out
	}
	if info, err := s.sessions().Inspect(id); err == nil {
		out.Name, out.Address, out.Room, out.RoomID, out.Hue = info.EffectiveName(), info.Address(), info.Room, info.RoomID, NameHue(info.Address())
	}
	return out
}
