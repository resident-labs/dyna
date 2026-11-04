package engine

import (
	"context"
	"time"
)

const replayRetryWindow = 24 * time.Hour

// ExpireReplayKeys deletes the client IDs that are past the retry window.
func (s *Service) ExpireReplayKeys(ctx context.Context) error {
	return s.store.ExpireReplayKeys(ctx, s.now().Add(-replayRetryWindow))
}
