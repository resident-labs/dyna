package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
)

// Accounts lists owner-visible account fields, never proxy credential IDs.
func (s *Store) Accounts(ctx context.Context) ([]contract.Account, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT a.id,a.provider,a.label,r.room FROM accounts a LEFT JOIN account_rooms r ON r.account=a.id ORDER BY a.id,r.room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []contract.Account{}
	for rows.Next() {
		var account contract.Account
		var room sql.NullString
		if err := rows.Scan(&account.ID, &account.Provider, &account.Label, &room); err != nil {
			return nil, err
		}
		if len(accounts) == 0 || accounts[len(accounts)-1].ID != account.ID {
			account.State = contract.AccountConnected
			account.Rooms = []contract.RoomID{}
			accounts = append(accounts, account)
		}
		if room.Valid {
			accounts[len(accounts)-1].Rooms = append(accounts[len(accounts)-1].Rooms, contract.RoomID(room.String))
		}
	}
	return accounts, rows.Err()
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
			if _, err := tx.ExecContext(ctx, "INSERT INTO account_rooms(account,room) VALUES(?,?)", account, room); err != nil {
				return err
			}
		}
		return nil
	})
}
