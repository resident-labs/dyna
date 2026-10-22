package view

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
)

// MergeControl applies the existing merge controls for the authenticated caller.
func (s *Service) MergeControl(ctx context.Context, roomRef, action string, args, without []string) error {
	rm, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, rm.ID)
	if err != nil {
		return err
	}
	return s.Watcher().MergeControl(authority.WithActor(ctx, actor), rm.ID, action, args, without)
}

// MergeControlStatus includes the same in-memory fairness order as the worker.
func (s *Service) MergeControlStatus(roomRef string) (watch.MergeControlStatus, error) {
	rm, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return watch.MergeControlStatus{}, err
	}
	return s.Watcher().MergeControlStatus(rm.ID)
}
