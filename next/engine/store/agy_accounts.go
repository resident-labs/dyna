package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// AgyAccount is an added Antigravity account: an opaque id and a label. Its sign-in is a stored token file named by the id.
type AgyAccount struct {
	ID      contract.AccountID
	Label   string
	Created time.Time
}

// AgyAccounts lists the added Antigravity accounts, oldest first.
func (s *Store) AgyAccounts(ctx context.Context) ([]AgyAccount, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT id,label,created FROM agy_accounts ORDER BY created,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []AgyAccount
	for rows.Next() {
		var account AgyAccount
		var created int64
		if err := rows.Scan(&account.ID, &account.Label, &created); err != nil {
			return nil, err
		}
		account.Created = time.UnixMilli(created)
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// AgyAccountExists says whether id names an added Antigravity account.
func (s *Store) AgyAccountExists(ctx context.Context, id contract.AccountID) (exists bool, err error) {
	err = s.read.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agy_accounts WHERE id=?)", id).Scan(&exists)
	return exists, err
}

// NewAgyAccountID is a fresh opaque id for an added Antigravity account. It is also the name of the stored token file.
func NewAgyAccountID() (contract.AccountID, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return contract.AccountID("agy_" + hex.EncodeToString(raw[:])), nil
}

// AddAgyAccount records an added account under an id the caller chose with NewAgyAccountID.
func (s *Store) AddAgyAccount(ctx context.Context, id contract.AccountID, label string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO agy_accounts(id,label,created) VALUES(?,?,?)", id, label, now.UnixMilli())
		return err
	})
}

// RenameAgyAccount changes an added account's label, or returns ErrNotFound.
func (s *Store) RenameAgyAccount(ctx context.Context, id contract.AccountID, label string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE agy_accounts SET label=? WHERE id=?", label, id)
		return noRows(result, err, ErrNotFound)
	})
}

// RemoveAgyAccount deletes an added account's row, or returns ErrNotFound. The token file is the caller's.
func (s *Store) RemoveAgyAccount(ctx context.Context, id contract.AccountID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM agy_accounts WHERE id=?", id)
		return noRows(result, err, ErrNotFound)
	})
}
