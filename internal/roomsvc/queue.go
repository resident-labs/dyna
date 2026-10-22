package roomsvc

import (
	"context"
	"dyna-agent/internal/subagent"
)

func (s *Service) SessionQueue(ref string) ([]subagent.QueueItem, error) {
	return s.sessions().SessionQueue(s.rooms(), ref)
}
func (s *Service) DeletePendingSteer(ref, id string) error {
	return s.sessions().DeletePendingSteer(s.rooms(), ref, id)
}

func (s *Service) DismissPendingPost(ctx context.Context, ref, id string, confirmed bool) error {
	return s.Notify.Sessions.DismissPendingPost(ctx, ref, id, confirmed)
}
