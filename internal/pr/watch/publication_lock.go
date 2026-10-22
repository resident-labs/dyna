package watch

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/store"
)

// The publication gate protects routing for one PR, not the PR state store.
// Always acquire this gate before state.lock, never from an update callback.
func (s *Service) tryPublicationGate(key string) (func(), bool, error) {
	if err := os.MkdirAll(s.Store.Root, 0700); err != nil {
		return nil, false, err
	}
	return store.TryLockAndRemove(filepath.Join(s.Store.Root, "publication-"+digest(key)+".lock"))
}

func (s *Service) publicationGate(ctx context.Context, key string) (func(), error) {
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, owned, err := s.tryPublicationGate(key)
		if err != nil {
			return nil, err
		}
		if owned {
			return unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// cleanupPublicationGates removes files left by older releases or crashes.
// A held publication stays in place. New gates remove themselves on release.
func (s *Service) cleanupPublicationGates() error {
	paths, err := filepath.Glob(filepath.Join(s.Store.Root, "publication-*.lock"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		unlock, owned, err := store.TryLockAndRemove(path)
		if err != nil {
			return err
		}
		if owned {
			unlock()
		}
	}
	return nil
}
