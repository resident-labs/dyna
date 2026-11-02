package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// diskLowWait is how long a host must stay at its floor before eviction starts. Closing cannot be undone and one sample can be wrong.
const diskLowWait = time.Minute

// CloseDormant closes at most 200 eligible leaves. The store rechecks each one.
func (s *Service) CloseDormant(ctx context.Context) error {
	ids, err := s.store.DormantCloseCandidates(ctx, s.now().Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	return s.closeIdle(ctx, ids)
}

// EvictDisk closes the oldest idle leaves of hosts at their disk floor. Close archives first, so work stays in refs.
func (s *Service) EvictDisk(ctx context.Context) error {
	ids, err := s.store.DiskEvictCandidates(ctx, s.now().Add(-diskLowWait))
	if err != nil {
		return err
	}
	return s.closeIdle(ctx, ids)
}

func (s *Service) closeIdle(ctx context.Context, ids []contract.SessionID) error {
	for _, id := range ids {
		session, err := s.store.Session(ctx, id)
		if err != nil {
			return err
		}
		if _, problem := s.closeSession(ctx, session, false, true); problem != nil {
			return fmt.Errorf("close idle session: %s", problem.Message)
		}
	}
	return nil
}
