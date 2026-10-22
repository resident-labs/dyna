package engine

import (
	"dyna-agent/internal/computers"
	"dyna-agent/internal/computerweb"
	"dyna-agent/internal/roomsvc"
)

// newComputerHost lists the room's open sessions and root, the room owner,
// whose one computer per room has the ID computers.ActorID gives it.
func newComputerHost(rooms *roomsvc.Service) *computerweb.Host {
	return &computerweb.Host{Sessions: func(roomID string) ([]computerweb.Session, error) {
		members, err := rooms.Members(roomID, false)
		if err != nil {
			return nil, err
		}
		out := []computerweb.Session{{ID: computers.ActorID("", roomID), Name: "root"}}
		for _, m := range members {
			if m.ID != "" && !m.Owner && !m.Closed {
				out = append(out, computerweb.Session{ID: m.ID, Name: m.Name})
			}
		}
		return out, nil
	}}
}
