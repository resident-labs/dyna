package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// The account each provider uses lives in dyna.db, beside the registry, so a
// removal clears it with the account. Scope is "" for the global row, or a room
// ID. A room row overrides the global row.
const activeSchema = `CREATE TABLE IF NOT EXISTS account_active (
 provider TEXT NOT NULL,
 scope TEXT NOT NULL DEFAULT '',
 account TEXT NOT NULL,
 PRIMARY KEY (provider, scope)
);`

// activeKey names a previous release's per-harness selection in state_mirrors.
func activeKey(harness string) string { return "accounts/active/" + harness }

// validActiveID reports whether id is an account ID or empty (no account).
func validActiveID(id string) bool {
	if id == "" {
		return true
	}
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil
}

// forgetActive clears every selection of account id.
func forgetActive(ctx context.Context, conn *sql.Conn, id string) error {
	_, err := conn.ExecContext(ctx, "UPDATE account_active SET account = '' WHERE account = ?", id)
	return err
}

// Active returns the ID of the account provider uses in scope, or empty.
func (s *Store) Active(provider Provider, scope string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var id string
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		var err error
		id, err = ReadActive(ctx, q, provider, scope)
		return err
	})
	return id, err
}

// ActiveFor returns the ID of the account provider uses in roomID: its room
// row when it has one, else the global row.
func (s *Store) ActiveFor(provider Provider, roomID string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var id string
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		if roomID != "" {
			room, found, err := readActiveRow(ctx, q, provider, roomID)
			if err != nil || found {
				id = room
				return err
			}
		}
		var err error
		id, err = ReadActive(ctx, q, provider, "")
		return err
	})
	return id, err
}

// ReadActive reads the saved selection through the caller's database snapshot.
// It does not open another connection.
func ReadActive(ctx context.Context, q statewrite.Reader, provider Provider, scope string) (string, error) {
	id, _, err := readActiveRow(ctx, q, provider, scope)
	return id, err
}

// readActiveRow reads one row and reports whether the row exists, so a room row
// that names no account still overrides the global row.
func readActiveRow(ctx context.Context, q statewrite.Reader, provider Provider, scope string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT account FROM account_active WHERE provider = ? AND scope = ?", provider, scope).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// SetActive records that provider uses account id in scope, or none when id is
// empty.
func (s *Store) SetActive(provider Provider, scope, id string) error {
	if !validActiveID(id) {
		return errors.New("An account selection needs a saved account ID.")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return setActiveRow(ctx, conn, provider, scope, id)
	})
}

func setActiveRow(ctx context.Context, conn *sql.Conn, provider Provider, scope, id string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO account_active(provider, scope, account) VALUES (?, ?, ?)
ON CONFLICT(provider, scope) DO UPDATE SET account = excluded.account`, provider, scope, id)
	return err
}
