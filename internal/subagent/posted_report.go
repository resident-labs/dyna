package subagent

import (
	"slices"
)

// An explicit post can supply the result for a harness that owes no final text.
// Read the current obligation, including live steering, before empty-output
// recovery.
// turnOwesNoReport reports a turn with no saved report obligation, so an empty
// final response after a clean stop is the quiet end it was told to give.
func (m *Manager) turnOwesNoReport(id string, n int) bool {
	turn, err := m.store().ReadTurn(id, n)
	return err == nil && turn.Schema == "" && len(turn.ReportTo) == 0
}

func (m *Manager) turnAcceptsEmptyOutput(id string, n int) bool {
	store := m.store()
	turn, err := store.ReadTurn(id, n)
	if err != nil || turn.Schema != "" {
		return false
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
