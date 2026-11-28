package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// AgyUse is which added Antigravity account every agy session uses, and the state of a switch.
type AgyUse struct {
	// Active is the account agy sessions use; empty until the signed-in account is imported or one is switched to.
	Active contract.AccountID
	// Held says a switch is in progress or was left unfinished: while it is set the gate gives no agy session a turn.
	Held bool
	// Target is the account the held switch is moving to, and StuckHost the host it stopped at, when it did.
	Target    contract.AccountID
	StuckHost contract.HostName
}

// AgyUse reads the active account and the switch state.
func (s *Store) AgyUse(ctx context.Context) (AgyUse, error) {
	var use AgyUse
	err := s.read.QueryRowContext(ctx, "SELECT active,held,target,stuck_host FROM agy_account").Scan(&use.Active, &use.Held, &use.Target, &use.StuckHost)
	if errors.Is(err, sql.ErrNoRows) {
		return AgyUse{}, nil
	}
	return use, err
}

// agyRow makes sure the one agy_account row exists.
func agyRow(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO agy_account(one) VALUES(1) ON CONFLICT DO NOTHING")
	return err
}

// BeginAgySwitch holds every agy session's dispatch and names the account the switch moves to. It reports whether this call set the hold:
// a switch an earlier run left unfinished is already held, and only a call for its own target may continue it (the engine refuses the others).
func (s *Store) BeginAgySwitch(ctx context.Context, target contract.AccountID) (fresh bool, err error) {
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		var held bool
		if err := tx.QueryRowContext(ctx, "SELECT held FROM agy_account WHERE one=1").Scan(&held); err != nil {
			return err
		}
		fresh = !held
		_, err := tx.ExecContext(ctx, "UPDATE agy_account SET held=1,target=?,stuck_host='' WHERE one=1", target)
		return err
	})
	return fresh, err
}

// AbortAgySwitch releases a switch that changed nothing, and wakes the agy sessions it held.
func (s *Store) AbortAgySwitch(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET held=0,target='',stuck_host='' WHERE one=1"); err != nil {
			return err
		}
		return s.wakeAgySessions(ctx, tx)
	})
}

// StickAgySwitch records the host a held switch stopped at.
func (s *Store) StickAgySwitch(ctx context.Context, host contract.HostName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE agy_account SET stuck_host=? WHERE one=1", host)
		return err
	})
}

// FinishAgySwitch makes the account active and releases the held sessions, in one transaction. A session whose failed turn waits for the
// old account's reset is due now, when the new account is not limited, so the retry sweep wakes it under its usual rules (a stopped or
// cancelled timer, -1, and a paused room stay as they are).
func (s *Store) FinishAgySwitch(ctx context.Context, id contract.AccountID, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET active=?,held=0,target='',stuck_host='' WHERE one=1", id); err != nil {
			return err
		}
		limited, err := s.agyLimited(ctx, tx, now)
		if err != nil {
			return err
		}
		if !limited {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET model_retry_at=?1 WHERE model_retry_at>?1 AND state='open' AND closed_at IS NULL AND id IN (SELECT session FROM boxes WHERE harness=?2)", now.UnixMilli(), contract.HarnessAgy); err != nil {
				return err
			}
		}
		return s.wakeAgySessions(ctx, tx)
	})
}

// ActivateAgyAccount makes the imported account active: the sign-in the engine host holds is its sign-in. The limit row of "main", the
// account as it was known before, is the imported account's now.
func (s *Store) ActivateAgyAccount(ctx context.Context, id contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET active=? WHERE one=1", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE agy_limits SET account=? WHERE account='main'", id)
		return err
	})
}

// AgyRunningTurns counts the turns that are open on agy boxes.
func (s *Store) AgyRunningTurns(ctx context.Context) (n int, err error) {
	err = s.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM turns t JOIN boxes b ON b.session=t.session WHERE t.ended_at IS NULL AND b.harness=?", contract.HarnessAgy).Scan(&n)
	return n, err
}

// AgyAccountInUse says the account is the active one or a held switch's target: its token file is in use.
func (s *Store) AgyAccountInUse(ctx context.Context, id contract.AccountID) (bool, error) {
	use, err := s.AgyUse(ctx)
	return err == nil && (use.Active == id || use.Held && use.Target == id), err
}
