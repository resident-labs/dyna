package subagent

// ActiveRoomIDs returns rooms with a held turn lock or a durable pending turn.
// It uses the same shared-lock probe as Info and never repairs or reports turns.
func (s *Store) ActiveRoomIDs() map[string]bool {
	live := map[string]bool{}
	s, ids, _ := s.listing(true)
	for _, id := range ids {
		m, entry, err := s.cachedMeta(id)
		if err != nil || m.RoomID == "" || !m.Closed.IsZero() {
			continue
		}
		if s.lockHeld(m.ID) {
			live[m.RoomID] = true
			continue
		}
		if m.Turns > 0 {
			turn, err := s.cachedTurn(m.ID, m.Turns, entry)
			if err == nil && !turn.done && turn.pending {
				live[m.RoomID] = true
			}
		}
	}
	return live
}
