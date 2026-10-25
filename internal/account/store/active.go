package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// The account each harness uses for each provider lives in dyna.db, beside the
// registry, so a removal clears it with the account. Harness is part of the key:
// two harnesses can run one provider (Claude Code and Pi both run anthropic) and
// their credential formats cannot be exchanged yet, so each keeps its own
// choice. Scope is "" for the global row, or a room ID. A room row overrides the
// global row of the same harness and provider.
const activeSchema = `CREATE TABLE IF NOT EXISTS account_active (
 provider TEXT NOT NULL,
 harness TEXT NOT NULL DEFAULT '',
 scope TEXT NOT NULL DEFAULT '',
 account TEXT NOT NULL,
 PRIMARY KEY (provider, harness, scope)
);`

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

// Selection is one saved account selection: the account of a provider that a
// harness uses in a scope, where scope is "" for the global row or a room ID.
type Selection struct {
	Provider Provider
	Harness  string
	Scope    string
	Account  string
}

// Selections reads every saved selection, so a reader can place each account in
// its scopes without one query per account. A row that names no account is kept:
// it overrides the global row with no selection of its own.
func (s *Store) Selections() ([]Selection, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var out []Selection
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT provider, harness, scope, account FROM account_active")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sel Selection
			if err := rows.Scan(&sel.Provider, &sel.Harness, &sel.Scope, &sel.Account); err != nil {
				return err
			}
			out = append(out, sel)
		}
		return rows.Err()
	})
	return out, err
}

// Active returns the ID of the account harness h uses for provider in scope, or
// empty.
func (s *Store) Active(provider Provider, harness, scope string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var id string
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		var err error
		id, err = ReadActive(ctx, q, provider, harness, scope)
		return err
	})
	return id, err
}

// ActiveFor returns the ID of the account harness h uses for provider in
// roomID: its room row when it has one, else the global row.
func (s *Store) ActiveFor(provider Provider, harness, roomID string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var id string
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		var err error
		id, _, err = ReadActiveFor(ctx, q, provider, harness, roomID)
		return err
	})
	return id, err
}

// ReadActive reads the saved selection through the caller's database snapshot.
// It does not open another connection.
func ReadActive(ctx context.Context, q statewrite.Reader, provider Provider, harness, scope string) (string, error) {
	id, _, err := readActiveRow(ctx, q, provider, harness, scope)
	return id, err
}

// ReadActiveFor reads the selection harness h uses in roomID through the
// caller's snapshot: the room row when it exists, else the global row. found
// reports whether either row exists, so a row that names no account is not
// mistaken for no row.
func ReadActiveFor(ctx context.Context, q statewrite.Reader, provider Provider, harness, roomID string) (id string, found bool, err error) {
	if roomID != "" {
		if id, found, err = readActiveRow(ctx, q, provider, harness, roomID); err != nil || found {
			return id, found, err
		}
	}
	return readActiveRow(ctx, q, provider, harness, "")
}

// readActiveRow reads one row and reports whether the row exists, so a room row
// that names no account still overrides the global row.
func readActiveRow(ctx context.Context, q statewrite.Reader, provider Provider, harness, scope string) (string, bool, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT account FROM account_active WHERE provider = ? AND harness = ? AND scope = ?", provider, harness, scope).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// SetActive records that harness h uses account id for provider in scope, or
// none when id is empty.
func (s *Store) SetActive(provider Provider, harness, scope, id string) error {
	if !validActiveID(id) {
		return errors.New("An account selection needs a saved account ID.")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return setActiveRow(ctx, conn, provider, harness, scope, id)
	})
}

func setActiveRow(ctx context.Context, conn *sql.Conn, provider Provider, harness, scope, id string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO account_active(provider, harness, scope, account) VALUES (?, ?, ?, ?)
ON CONFLICT(provider, harness, scope) DO UPDATE SET account = excluded.account`, provider, harness, scope, id)
	return err
}
