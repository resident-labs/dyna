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
	return queryAll(ctx, s.Read(), func(rows scanner) (session Session, err error) {
		err = rows.Scan(&session.ID, &session.Room)
		return
	}, "SELECT id,room FROM sessions WHERE profile=? AND closed_at IS NULL ORDER BY created_at,id", name)
}

// SetSessionProfile changes the profile and delivers the change atomically.
func (s *Store) SetSessionProfile(ctx context.Context, target contract.SessionID, from, profile contract.ProfileName, event contract.Event, harness *HarnessSwitch) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		tree, err := loadTree(ctx, tx, event.Room)
		if err != nil {
			return err
		}
		if !tree.CanManage(event.From, target) {
			return ErrTreeForbidden
		}
		// The caller decided from the profile it read, so another change in between makes that decision stale.
		var current contract.ProfileName
		if err := tx.QueryRowContext(ctx, "SELECT profile FROM sessions WHERE id=? AND closed_at IS NULL", target).Scan(&current); err != nil {
			return err
		}
		if current != from {
			return ErrProfileChanged
		}
		if harness != nil {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND closed_at IS NULL", target).Scan(&state); err != nil {
				return err
			}
			if state != "open" {
				return ErrSessionClosing
			}
			if harness.RequireHandoff {
				var can bool
				if err := tx.QueryRowContext(ctx, "SELECT can_handoff FROM boxes WHERE session=?", target).Scan(&can); err != nil {
					return err
				}
				if !can {
					return ErrNoHandoff
				}
			}
			// The handoff is due from the next turn, not from one already running.
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET handoff=MAX(1,(SELECT COALESCE(MAX(seq),0) FROM events)) WHERE id=?", target); err != nil {
				return err
			}
		}
		return s.setProfile(ctx, tx, target, profile, event, harness != nil)
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
			if err := s.setProfile(ctx, tx, move.Session, profile, move.Event, false); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) setProfile(ctx context.Context, tx *sql.Tx, target contract.SessionID, profile contract.ProfileName, event contract.Event, harnessSwitch bool) error {
	var outcome contract.TurnOutcome
	err := tx.QueryRowContext(ctx, "SELECT outcome FROM turns WHERE session=? AND ended_at IS NOT NULL ORDER BY ended_at DESC, started_seq DESC LIMIT 1", target).Scan(&outcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A failed turn wakes to carry its input to the new profile. A harness switch leaves a scheduled wake (a usage limit's reset) to
	// its time: the switch is not a reason to start it early.
	wake := outcome == contract.TurnFailed
	if wake && harnessSwitch {
		var scheduled bool
		if err := tx.QueryRowContext(ctx, "SELECT model_retry_at>0 FROM sessions WHERE id=?", target).Scan(&scheduled); err != nil {
			return err
		}
		wake = !scheduled
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET profile=? WHERE id=?", profile, target); err != nil {
		return err
	}
	seq, err := s.AppendEvent(tx, event)
	if err != nil {
		return err
	}
	return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: target}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: wake})
}

// HarnessSwitch says a session's profile change is a change of harness, which starts its native conversation again from a
// handoff; nil for a change within one harness.
type HarnessSwitch struct {
	// RequireHandoff holds the switch to a box that said it can hand a conversation over, read in the same transaction.
	RequireHandoff bool
}

// TurnHarness is the harness of the box a turn was dispatched on, which is the one that served it: dispatch waits for a box made
// for the session's current harness. Empty for a turn from before the column existed.
func (s *Store) TurnHarness(ctx context.Context, session contract.SessionID, turn contract.TurnID) (contract.Harness, error) {
	var harness contract.Harness
	err := s.Read().QueryRowContext(ctx, "SELECT harness FROM turns WHERE session=? AND id=?", session, turn).Scan(&harness)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return harness, err
}

var (
	// ErrProfileChanged says the session's profile changed after the caller read it, so what it decided is stale.
	ErrProfileChanged = errors.New("session profile changed meanwhile")
	// ErrNoHandoff says the session's box has not said it can hand a conversation over.
	ErrNoHandoff = errors.New("box cannot hand off")
)
