package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/store"
)

// A separate cross-process lock protects child edits as well as publication.
// It never holds the PR metadata lock or waits behind another publisher.
func (s *Service) mergeLock() (func(), error) {
	if err := os.MkdirAll(s.Store.Root, 0700); err != nil {
		return nil, err
	}
	unlock, owned, err := store.TryLock(filepath.Join(s.Store.Root, "merge-publish.lock"))
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, fmt.Errorf("another merge publisher is active; retry after it finishes")
	}
	return unlock, nil
}

// requirementLockWait bounds a requirement write's wait for a publication's
// final step. Tests shorten it to keep a refused write fast.
var requirementLockWait = 30 * time.Second

// The reviewer-requirement lock is narrower than the merge publication lock.
// A publisher holds it only across one batch's final readiness checks and its
// publication, so a requirement cannot land between those checks and the
// publication. Requirement writes take this lock instead of the merge
// publication lock, which a publisher holds for whole ticks while merge
// trials run for minutes (D-409).
func (s *Service) requirementLock(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, requirementLockWait)
	defer cancel()
	if err := os.MkdirAll(s.Store.Root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Store.Root, "reviewer-requirements.lock")
	unlock, err := store.LockContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("waiting for a publication's final step at %s: %w", path, err)
	}
	return unlock, nil
}

// Manual controls can arrive during a short automatic publication. Wait on the
// same lock, but bound this wait separately from the control's remaining work.
func (s *Service) mergeControlLock(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(s.Store.Root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Store.Root, "merge-publish.lock")
	unlock, err := store.LockContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("waiting for merge publisher at %s: %w", path, err)
	}
	return unlock, nil
}
