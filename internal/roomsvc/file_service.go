package roomsvc

import "dyna-agent/internal/filesvc"

func (s *Service) files() *filesvc.Service {
	return &filesvc.Service{Rooms: s.rooms(), SessionInRoom: s.SessionInRoom, AttachmentBytes: s.AttachmentBytes}
}
