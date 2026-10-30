//go:build !linux

package shells

import (
	"context"
	"time"
)

// SweepOrphanDirectories is unavailable where directory cookies are unsupported.
func (s *Store) SweepOrphanDirectories(ctx context.Context, now time.Time) (int, error) {
	return 0, nil
}
