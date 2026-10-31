package engine

import (
	"context"
	"errors"
	"fmt"
)

// ExpireArchives deletes expired remote archive refs before forgetting their records.
func (s *Service) ExpireArchives(ctx context.Context) error {
	archives, err := s.store.ExpiredArchives(ctx, s.now())
	if err != nil {
		return err
	}
	var failures []error
	for _, a := range archives {
		if a.Ref != "" {
			if a.Ref != "refs/dyna/archive/"+string(a.Session) {
				failures = append(failures, fmt.Errorf("invalid archive ref for %s", a.Session))
				continue
			}
			if err := s.repos.DeleteArchiveRef(ctx, a.Repo, a.Ref, a.Commit); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if err := s.store.ForgetArchive(ctx, a); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
