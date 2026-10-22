package shells

// ActiveRoomIDs returns rooms with running or starting shell commands.
func (s *Store) ActiveRoomIDs() map[string]bool {
	live := map[string]bool{}
	list, _ := s.List(false)
	for _, in := range list {
		if in.RoomID != "" {
			live[in.RoomID] = true
		}
	}
	return live
}
