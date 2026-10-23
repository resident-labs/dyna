package subagent

import (
	"context"

	"dyna-agent/internal/room"
)

// Rename changes an open session's label. Its ID, history, goals and PR links
// stay the same. The name must be unused by an open session in the room.
func (m *Manager) Rename(ctx context.Context, ref, name string) (Info, error) {
	info, err := m.authorityTarget(ctx, "subagent rename", ref)
	if err != nil {
		return Info{}, err
	}
	store := m.store()
	if err := ValidateName(name); err != nil {
		return Info{}, err
	}
	unlock, err := lockNamesContext(ctx, store)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	meta, err := store.ReadMeta(info.ID)
	if err != nil {
		return Info{}, err
	}
	if err := checkServiceName(name, meta.Role); err != nil {
		return Info{}, err
	}
	if name == meta.EffectiveName() {
		return store.Info(info.ID)
	}
	taken, err := store.nameTaken(meta.RoomID, name)
	if err != nil {
		return Info{}, err
	}
	if taken {
		return Info{}, nameTakenError{room: meta.Room, name: name}
	}
	if _, err := store.UpdateMeta(info.ID, func(s *Meta) { s.Name = name }); err != nil {
		return Info{}, err
	}
	m.noteRename(meta, name)
	return store.Info(info.ID)
}

// noteRename tells the parent in one quiet note. The parent is not woken.
func (m *Manager) noteRename(meta Meta, newName string) {
	if meta.ParentSession == "" {
		return
	}
	parent, err := m.store().ReadMeta(meta.ParentSession)
	if err != nil || !parent.Closed.IsZero() {
		return
	}
	_, _ = m.rooms().PostEvent(meta.RoomID, room.SessionEvent{
		Type:    room.SessionRenamed,
		Session: meta.Address(),
		OldName: meta.EffectiveName(),
		Name:    newName,
	}, parent.Address())
}
