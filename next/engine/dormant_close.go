package engine

import (
	"context"
	"fmt"
	"time"
)

// CloseDormant closes at most 50 eligible leaves. The store rechecks each one.
func (s *Service) CloseDormant(ctx context.Context) error {
	ids, err := s.store.DormantCloseCandidates(ctx, s.now().Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	for _, id := range ids {
		session, err := s.store.Session(ctx, id)
		if err != nil {
			return err
		}
		if problem := s.closeSession(ctx, session, false, true); problem != nil {
			return fmt.Errorf("close dormant session: %s", problem.Message)
		}
	}
	return nil
}
