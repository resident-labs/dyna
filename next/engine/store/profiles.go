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

// SessionsOnProfile returns the open sessions that use a profile. The caller holds the profile write lock.
func (s *Store) SessionsOnProfile(ctx context.Context, name contract.ProfileName) ([]Session, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,room FROM sessions WHERE profile=? AND closed_at IS NULL ORDER BY created_at,id", name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.Room); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
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
		return s.setProfile(ctx, tx, target, profile, event)
	})
}

// ProfileMove is one session's profile change and the event that records it.
type ProfileMove struct {
	Session contract.SessionID
	Event   contract.Event
}

// MoveProfile moves sessions to a profile in one transaction.
func (s *Store) MoveProfile(ctx context.Context, profile contract.ProfileName, moves []ProfileMove) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, move := range moves {
			if err := s.setProfile(ctx, tx, move.Session, profile, move.Event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) setProfile(ctx context.Context, tx *sql.Tx, target contract.SessionID, profile contract.ProfileName, event contract.Event) error {
	var outcome contract.TurnOutcome
	err := tx.QueryRowContext(ctx, "SELECT outcome FROM turns WHERE session=? AND ended_at IS NOT NULL ORDER BY ended_at DESC, started_seq DESC LIMIT 1", target).Scan(&outcome)
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
}
