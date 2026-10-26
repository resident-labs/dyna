package subagent

// HoldsTurnWork reports whether session id has a turn running or queued, or
// input waiting for its next turn. A session it cannot read counts as busy, so
// memory relief never stops a process on a guess.
func (m *Manager) HoldsTurnWork(id string) bool {
	store := m.store()
	info, err := store.Info(id)
	if err != nil || info.Status == StatusRunning || info.Status == StatusQueued {
		return true
	}
	meta, err := store.ReadMeta(id)
	if err != nil {
		return true
	}
	pending, err := m.pendingRoomWork(meta)
	return err != nil || pending
}
