package engine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const replayRetryWindow = 24 * time.Hour

// ExpireArchives deletes expired replay keys and remote archive refs.
func (s *Service) ExpireArchives(ctx context.Context) error {
	now := s.now()
	if err := s.store.ExpireReplayKeys(ctx, now.Add(-replayRetryWindow)); err != nil {
		return err
	}
	archives, err := s.store.ExpiredArchives(ctx, now)
	if err != nil {
		return err
	}
	var failures []error
	for _, a := range archives {
		if a.Ref != "" {
			if err := s.store.RetryArchive(ctx, a, s.now().Add(time.Hour)); err != nil {
				failures = append(failures, err)
				continue
			}
			if a.Ref != "refs/dyna/archive/"+string(a.Session) {
				failures = append(failures, fmt.Errorf("invalid archive ref for %s", a.Session))
				continue
			}
			if err := s.repos.DeleteArchiveRef(ctx, a.Repo, a.Ref, a.Commit); err != nil {
				failures = append(failures, err)
				if ctx.Err() != nil {
					return errors.Join(failures...)
				}
				continue
			}
		}
		if err := s.store.ForgetArchive(ctx, a); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
