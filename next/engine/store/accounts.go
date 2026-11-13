package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
)

// AccountBinding keeps the private proxy identity inside the engine.
type AccountBinding = private.AccountBinding

// Accounts lists accounts and their private proxy bindings.
func (s *Store) Accounts(ctx context.Context) ([]AccountBinding, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT a.id,a.provider,a.label,a.proxy_id,r.room,r.preferred FROM accounts a LEFT JOIN (SELECT account,room,0 AS preferred FROM account_rooms UNION ALL SELECT account,room,1 FROM account_prefs) r ON r.account=a.id ORDER BY a.id,r.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []AccountBinding{}
	for rows.Next() {
		var binding AccountBinding
		account := &binding.Account
		var room sql.NullString
		var preferred sql.NullBool
		if err := rows.Scan(&account.ID, &account.Provider, &account.Label, &binding.ProxyID, &room, &preferred); err != nil {
			return nil, err
		}
		if len(accounts) == 0 || accounts[len(accounts)-1].Account.ID != account.ID {
			account.State = contract.AccountUnknown
			account.Rooms = []contract.RoomID{}
			account.Preferred = []contract.RoomID{}
			accounts = append(accounts, binding)
		}
		last := &accounts[len(accounts)-1].Account
		switch {
		case !room.Valid:
		case preferred.Bool:
			last.Preferred = append(last.Preferred, contract.RoomID(room.String))
		default:
			last.Rooms = append(last.Rooms, contract.RoomID(room.String))
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

// RenameAccount changes an account's label. A label is only a display name: two
// accounts may share one, and nothing joins on it.
func (s *Store) RenameAccount(ctx context.Context, id contract.AccountID, label string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE accounts SET label=? WHERE id=?", label, id)
		return noRows(result, err, ErrNotFound)
	})
}

// BindAccount associates an opaque account with a private proxy credential.
func (s *Store) BindAccount(ctx context.Context, id contract.AccountID, proxyID string, provider contract.AccountProvider, label string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) SELECT ?,id,?,? FROM proxy_auths WHERE id=?", id, provider, label, proxyID)
		return noRows(result, err, sql.ErrNoRows)
	})
}

// ErrPreferUnserved refuses a preference for a room the account's allowlist excludes.
var ErrPreferUnserved = errors.New("a preferred room must be one the account serves")

// SetAccountRooms replaces the allowlist and the rooms that prefer the account in one
// transaction, so a preference never outlives the allowlist entry it needs.
func (s *Store) SetAccountRooms(ctx context.Context, account contract.AccountID, rooms, preferred []contract.RoomID) error {
	if len(rooms) > 0 && slices.ContainsFunc(preferred, func(room contract.RoomID) bool { return !slices.Contains(rooms, room) }) {
		return ErrPreferUnserved
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		var id contract.AccountID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE id=?", account).Scan(&id); err != nil {
			return err
		}
		for table, list := range map[string][]contract.RoomID{"account_rooms": rooms, "account_prefs": preferred} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE account=?", account); err != nil {
				return err
			}
			for _, room := range list {
				if err := refuseNewWork(ctx, tx, room); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"(account,room) VALUES(?,?)", account, room); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// EligibleAccountProxyIDs reads a room's complete credential policy in one snapshot.
func (s *Store) EligibleAccountProxyIDs(ctx context.Context, room contract.RoomID) ([]string, error) {
	return s.accountProxyIDs(ctx, `SELECT a.proxy_id FROM accounts a WHERE NOT EXISTS (SELECT 1 FROM account_rooms r WHERE r.account=a.id) OR EXISTS (SELECT 1 FROM account_rooms r WHERE r.account=a.id AND r.room=?) ORDER BY a.id`, room)
}

// PreferredAccountProxyIDs lists the credentials a room prefers. The proxy tries them
// first among the eligible ones.
func (s *Store) PreferredAccountProxyIDs(ctx context.Context, room contract.RoomID) ([]string, error) {
	return s.accountProxyIDs(ctx, `SELECT a.proxy_id FROM accounts a JOIN account_prefs p ON p.account=a.id WHERE p.room=? ORDER BY a.id`, room)
}

func (s *Store) accountProxyIDs(ctx context.Context, query string, room contract.RoomID) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, query, room)
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
