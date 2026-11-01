package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// SessionAccountPin reads a session's chosen account; empty means Automatic. The row
// outlives its account, so the owner can be told the choice no longer works.
func (s *Store) SessionAccountPin(ctx context.Context, session contract.SessionID) (contract.AccountID, error) {
	var account contract.AccountID
	err := s.Read().QueryRowContext(ctx, "SELECT account FROM session_accounts WHERE session=?", session).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return account, err
}

// SetSessionAccountPin stores the choice; an empty account returns to Automatic.
func (s *Store) SetSessionAccountPin(ctx context.Context, session contract.SessionID, account contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if account == "" {
			_, err := tx.ExecContext(ctx, "DELETE FROM session_accounts WHERE session=?", session)
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO session_accounts(session,account) VALUES(?,?) ON CONFLICT(session) DO UPDATE SET account=excluded.account", session, account)
		return err
	})
}

// SessionPinnedProxyID reads the private proxy binding of a session's choice. It is
// empty on Automatic and when the account is gone.
func (s *Store) SessionPinnedProxyID(ctx context.Context, session contract.SessionID) (string, error) {
	var proxyID string
	err := s.Read().QueryRowContext(ctx, "SELECT a.proxy_id FROM session_accounts p JOIN accounts a ON a.id=p.account WHERE p.session=?", session).Scan(&proxyID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return proxyID, err
}

// PinnedAccounts lists the accounts an open session has chosen.
func (s *Store) PinnedAccounts(ctx context.Context) ([]contract.AccountID, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT DISTINCT p.account FROM session_accounts p JOIN sessions s ON s.id=p.session WHERE s.closed_at IS NULL ORDER BY p.account")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []contract.AccountID{}
	for rows.Next() {
		var account contract.AccountID
		if err := rows.Scan(&account); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}
