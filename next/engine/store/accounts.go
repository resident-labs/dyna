package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
)

// AccountBinding keeps the private proxy identity inside the engine.
type AccountBinding = private.AccountBinding

// Accounts lists accounts and their private proxy bindings.
func (s *Store) Accounts(ctx context.Context) ([]AccountBinding, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT a.id,a.provider,a.label,a.proxy_id,r.room FROM accounts a LEFT JOIN account_rooms r ON r.account=a.id ORDER BY a.id,r.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []AccountBinding{}
	for rows.Next() {
		var binding AccountBinding
		account := &binding.Account
		var room sql.NullString
		if err := rows.Scan(&account.ID, &account.Provider, &account.Label, &binding.ProxyID, &room); err != nil {
			return nil, err
		}
		if len(accounts) == 0 || accounts[len(accounts)-1].Account.ID != account.ID {
			account.State = contract.AccountUnknown
			account.Rooms = []contract.RoomID{}
			accounts = append(accounts, binding)
		}
		if room.Valid {
			accounts[len(accounts)-1].Account.Rooms = append(accounts[len(accounts)-1].Account.Rooms, contract.RoomID(room.String))
		}
	}
	return accounts, rows.Err()
}

// AccountProxyID returns a private binding for internal proxy operations.
func (s *Store) AccountProxyID(ctx context.Context, id contract.AccountID) (string, error) {
	var proxyID string
	err := s.read.QueryRowContext(ctx, "SELECT proxy_id FROM accounts WHERE id=?", id).Scan(&proxyID)
	return proxyID, err
}

// RemoveAccount deletes a row after its proxy credential is gone.
func (s *Store) RemoveAccount(ctx context.Context, id contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM accounts WHERE id=?", id)
		return err
	})
}

// BindAccount associates an opaque account with a private proxy credential.
func (s *Store) BindAccount(ctx context.Context, id contract.AccountID, proxyID string, provider contract.AccountProvider, label string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) SELECT ?,id,?,? FROM proxy_auths WHERE id=?", id, provider, label, proxyID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// SetAccountRooms replaces the allowlist in one transaction.
func (s *Store) SetAccountRooms(ctx context.Context, account contract.AccountID, rooms []contract.RoomID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var id contract.AccountID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE id=?", account).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM account_rooms WHERE account=?", account); err != nil {
			return err
		}
		for _, room := range rooms {
			if err := refuseNewWork(ctx, tx, room); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO account_rooms(account,room) VALUES(?,?)", account, room); err != nil {
				return err
			}
		}
		return nil
	})
}

// EligibleAccountProxyIDs reads a room's complete credential policy in one snapshot.
func (s *Store) EligibleAccountProxyIDs(ctx context.Context, room contract.RoomID) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT a.proxy_id FROM accounts a WHERE NOT EXISTS (SELECT 1 FROM account_rooms r WHERE r.account=a.id) OR EXISTS (SELECT 1 FROM account_rooms r WHERE r.account=a.id AND r.room=?) ORDER BY a.id`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
