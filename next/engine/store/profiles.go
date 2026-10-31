package store

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// ProfileInUse reports whether an open session references the profile.
func (s *Store) ProfileInUse(ctx context.Context, name contract.ProfileName) (bool, error) {
	var inUse bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE profile=? AND closed_at IS NULL)", name).Scan(&inUse)
	return inUse, err
}
