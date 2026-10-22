package view

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
)

func (s *Service) RoomChangePolicy(roomRef string) (watch.ChangePolicy, error) {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return watch.ChangePolicy{}, err
	}
	return s.Watcher().RoomChangePolicy(room.ID)
}
func (s *Service) SetRoomChangePolicy(ctx context.Context, roomRef string, p watch.ChangePolicy) error {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, room.ID)
	if err != nil {
		return err
	}
	return s.Watcher().SetRoomChangePolicy(room.ID, actor, p)
}

// SetChangePolicyOverride sets one change's policy override; its author, the
// author's ancestors, the owner and the room's root may set it.
func (s *Service) SetChangePolicyOverride(ctx context.Context, roomRef, changeID string, o watch.PolicyOverride) error {
	p, r, err := s.authorPRInRoom(ctx, "SetChangePolicyOverride", roomRef, changeID)
	if err != nil {
		return err
	}
	return p.SetChangePolicyOverride(changeID, r.Author, o)
}
