package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

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
	return queryAll(ctx, s.Read(), func(rows scanner) (session Session, err error) {
		err = rows.Scan(&session.ID, &session.Room)
		return
	}, "SELECT id,room FROM sessions WHERE profile=? AND closed_at IS NULL ORDER BY created_at,id", name)
}

// SetSessionProfile changes the profile and delivers the change atomically.
func (s *Store) SetSessionProfile(ctx context.Context, target contract.SessionID, profile contract.ProfileName, event contract.Event, harness *HarnessSwitch) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		if !tree.CanManage(event.From, target) {
			return ErrTreeForbidden
		}
		if harness != nil {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND closed_at IS NULL", target).Scan(&state); err != nil {
				return err
			}
			if state != "open" {
				return ErrSessionClosing
			}
			if len(harness.Profiles) > 0 {
				var open int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE profile IN (SELECT value FROM json_each(?)) AND closed_at IS NULL", mustJSON(harness.Profiles)).Scan(&open); err != nil {
					return err
				}
				if open >= harness.Limit {
					return ErrProfileFull
				}
			}
			// The handoff is due from the next turn, not from one already running; its account starts at the default.
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET agy_account='',handoff=MAX(1,(SELECT COALESCE(MAX(seq),0) FROM events)) WHERE id=?", target); err != nil {
				return err
			}
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

// HarnessSwitch says a session's profile change is a change of harness, which starts its native conversation again from a
// handoff. Profiles are the profiles of the agy account when the session moves onto agy, so the account's limit is held inside
// the same transaction; nil for a change within one harness.
type HarnessSwitch struct {
	Profiles []contract.ProfileName
	Limit    int
}

// AgyStreak returns the accounts that returned a usage limit since the session last completed a turn or heard from the owner,
// and whose reset has not passed.
func (s *Store) AgyStreak(ctx context.Context, session contract.SessionID, now time.Time) (map[string]bool, error) {
	limited := map[string]bool{}
	rows, err := s.Read().QueryContext(ctx, "SELECT account FROM agy_streak WHERE session=? AND reset_at>?", session, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var account string
		if err := rows.Scan(&account); err != nil {
			return nil, err
		}
		limited[account] = true
	}
	return limited, rows.Err()
}

// TurnPredatesHandoff says a turn started before the session's handoff became due, so the harness that serves it is the one the
// session left: the turn was running when the session changed harness.
func (s *Store) TurnPredatesHandoff(ctx context.Context, session contract.SessionID, turn contract.TurnID) (bool, error) {
	var predates bool
	err := s.Read().QueryRowContext(ctx, "SELECT s.handoff>0 AND s.handoff>=COALESCE(t.started_seq,0) FROM sessions s JOIN turns t ON t.session=s.id WHERE s.id=? AND t.id=?", session, turn).Scan(&predates)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return predates, err
}
