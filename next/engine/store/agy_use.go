package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// AgyUse is which added Antigravity account every agy session uses, and the state of a switch.
type AgyUse struct {
	// Active is the account agy sessions use; empty until the signed-in account is imported or one is switched to.
	Active contract.AccountID
	// Holder is the account whose sign-in the engine host's token file holds. It differs from Active only in a switch left half-done, after
	// the engine host took the new token and before every host did, and a take-back goes to it, never to Active.
	Holder contract.AccountID
	// Held says a switch is in progress or was left unfinished: while it is set the gate gives no agy session a turn.
	Held bool
	// Target is the account the held switch is moving to, and StuckHost the host it stopped at, when it did.
	Target    contract.AccountID
	StuckHost contract.HostName
}

// AgyUse reads the active account and the switch state.
func (s *Store) AgyUse(ctx context.Context) (AgyUse, error) {
	var use AgyUse
	err := s.read.QueryRowContext(ctx, "SELECT active,holder,held,target,stuck_host FROM agy_account").Scan(&use.Active, &use.Holder, &use.Held, &use.Target, &use.StuckHost)
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

// BeginAgySwitch holds every agy session's dispatch and names the account the switch moves to. A switch left unfinished may be begun again.
func (s *Store) BeginAgySwitch(ctx context.Context, target contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE agy_account SET held=1,target=?,stuck_host='' WHERE one=1", target)
		return err
	})
}

// AbortAgySwitch ends a switch that changed nothing: agy sessions are released, and those with a waiting input start. A switch an
// earlier run left half-done stays held, because the engine host holds a token that is not the active account's.
func (s *Store) AbortAgySwitch(ctx context.Context) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET held=CASE WHEN holder!='' AND holder!=active THEN 1 ELSE 0 END,target=CASE WHEN holder!='' AND holder!=active THEN target ELSE '' END,stuck_host='' WHERE one=1"); err != nil {
			return err
		}
		return s.wakeAgySessions(ctx, tx)
	})
}

// NoteAgyHolder records that the engine host's token file now holds the account's sign-in.
func (s *Store) NoteAgyHolder(ctx context.Context, id contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE agy_account SET holder=? WHERE one=1", id)
		return err
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

// FinishAgySwitch makes the account active, and releases the held sessions, in one transaction.
func (s *Store) FinishAgySwitch(ctx context.Context, id contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := agyRow(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET active=?1,holder=?1,held=0,target='',stuck_host='' WHERE one=1", id); err != nil {
			return err
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
		if _, err := tx.ExecContext(ctx, "UPDATE agy_account SET active=?1,holder=?1 WHERE one=1", id); err != nil {
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

// AgyAccountInUse says the account is the active one, holds the engine host's token, or is a switch's target: its token file is in use.
func (s *Store) AgyAccountInUse(ctx context.Context, id contract.AccountID) (bool, error) {
	use, err := s.AgyUse(ctx)
	return err == nil && use.Held && use.Target == id || err == nil && (use.Active == id || use.Holder == id), err
}
