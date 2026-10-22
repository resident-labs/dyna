package subagent

import (
	"slices"

	"dyna-agent/internal/profile"
)

// A Codex turn can finish without final text when it owes no report. An
// explicit post can supply the result for any harness. Read the current
// obligation, including live steering, before empty-output recovery.
func (m *Manager) turnAcceptsEmptyOutput(id string, n int) bool {
	store := m.store()
	turn, err := store.ReadTurn(id, n)
	if err != nil || turn.Schema != "" {
		return false
	}
	if turn.Harness == profile.HarnessCodex && len(turn.ReportTo) == 0 {
		return true
	}
	if len(turn.PostedTo) == 0 {
		return false
	}
	meta, err := store.ReadMeta(id)
	if err != nil {
		return false
	}
	if slices.Contains(turn.PostedTo, meta.Room) {
		return true
	}
	if meta.ParentSession != "" {
		parent, err := store.InspectDeliveryTarget(meta.ParentSession)
		if err == nil && parent.RoomID == meta.RoomID && slices.Contains(turn.PostedTo, parent.Address()) {
			return true
		}
	}
	root, open, err := m.rootMeta(meta.RoomID)
	return err == nil && open && slices.Contains(turn.PostedTo, root.Address())
}
