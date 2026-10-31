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

func (s *Store) ProfileUsage(ctx context.Context) (map[contract.ProfileName]int, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT profile, COUNT(*) FROM sessions WHERE closed_at IS NULL GROUP BY profile")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[contract.ProfileName]int)
	for rows.Next() {
		var name contract.ProfileName
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		counts[name] = count
	}
	return counts, rows.Err()
}
