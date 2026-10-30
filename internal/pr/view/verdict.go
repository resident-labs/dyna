package view

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
)

// VerdictResult carries a saved or obsolete verdict notice across the socket.
// These notices are successful command outcomes, not transport errors.
type VerdictResult struct {
	Notice string `json:"notice,omitempty"`
}

// RecordReviewResult uses the authenticated session as the reviewer. The
// watcher checks its assignment or room roster and the exact head inside the
// state update. The result is a typed review result; anything else is rejected.
func (s *Service) RecordReviewResult(ctx context.Context, roomRef, key string, result watch.ReviewResult) (VerdictResult, error) {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return VerdictResult{}, err
	}
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return VerdictResult{}, err
	}
	actor, err := authority.ActorIn(ctx, room.ID)
	if err != nil {
		return VerdictResult{}, err
	}
	switch actor.Role {
	case authority.Session, authority.Root:
		if actor.Session == "" {
			return VerdictResult{}, fmt.Errorf("a verdict needs an authenticated reviewer session")
		}
	default:
		return VerdictResult{}, fmt.Errorf("a verdict needs an authenticated reviewer session")
	}
	err = p.RecordReviewResult(ctx, key, actor.Session, result)
	if notice, ok := watch.VerdictNotice(err); ok {
		return VerdictResult{Notice: notice}, nil
	}
	return VerdictResult{}, err
}
