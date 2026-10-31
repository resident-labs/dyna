package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// ProfileInUse reports whether an open session references the profile.
func (s *Store) ProfileInUse(ctx context.Context, name contract.ProfileName) (bool, error) {
	var inUse bool
	// ProfileConfig.Remove holds its write lock until this read and removal finish.
	// All assignments release their read lock only after the store commits.
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

// SetSessionProfile changes the profile and delivers the change atomically.
func (s *Store) SetSessionProfile(ctx context.Context, target contract.SessionID, profile contract.ProfileName, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		if !tree.CanManage(event.From, target) {
			return ErrTreeForbidden
		}
		var outcome contract.TurnOutcome
		err = tx.QueryRowContext(ctx, "SELECT outcome FROM turns WHERE session=? AND ended_at IS NOT NULL ORDER BY ended_at DESC, started_seq DESC LIMIT 1", target).Scan(&outcome)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET profile=? WHERE id=?", profile, target); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: target}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: outcome == contract.TurnFailed})
	})
}
