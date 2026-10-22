package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// The account each harness uses lives in dyna.db, beside the registry, so a
// removal clears it with the account.
const activeSchema = `CREATE TABLE IF NOT EXISTS account_active (
 harness TEXT PRIMARY KEY,
 account TEXT NOT NULL
);`

func (s *Store) activePath(harness string) string {
	return filepath.Join(s.Root, harness, ".active.json")
}

func activeKey(harness string) string { return "accounts/active/" + harness }

// validActiveID reports whether id is an account ID or empty (no account).
func validActiveID(id string) bool {
	if id == "" {
		return true
	}
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil
}

// TODO: legacy/shim remove later — import retained selections only during migration.
func importActiveSelections(ctx context.Context, conn *sql.Conn, root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		harness := entry.Name()
		var exists bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM account_active WHERE harness = ?)", harness).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		// Prior mirror evidence also preserves an explicit clear, whose old row was deleted.
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM state_mirrors WHERE key = ? AND (version != '' OR COALESCE(pending, '') != ''))`, activeKey(harness)).Scan(&exists); err != nil {
			return err
		}
		if exists {
			if err := setActiveRow(ctx, conn, harness, ""); err != nil {
				return err
			}
			continue
		}
		_, err := statewrite.ImportMigration(ctx, conn, activeKey(harness), filepath.Join(root, harness, ".active.json"), func(conn *sql.Conn, raw []byte) error {
			var selection struct {
				ID string `json:"id"`
			}
			if len(raw) > 256 || json.Unmarshal(raw, &selection) != nil || !validActiveID(selection.ID) {
				return fmt.Errorf("invalid account selection for %s", harness)
			}
			return setActiveRow(ctx, conn, harness, selection.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func setActiveRow(ctx context.Context, conn *sql.Conn, harness, id string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO account_active(harness, account) VALUES (?, ?)
ON CONFLICT(harness) DO UPDATE SET account = excluded.account`, harness, id)
	return err
}

// forgetActive clears every harness selection of account id.
func forgetActive(ctx context.Context, conn *sql.Conn, id string) error {
	_, err := conn.ExecContext(ctx, "UPDATE account_active SET account = '' WHERE account = ?", id)
	return err
}

// Active returns the ID of the account that harness uses, or empty.
func (s *Store) Active(harness string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var id string
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		var err error
		id, err = ReadActive(ctx, q, harness)
		return err
	})
	return id, err
}

// ReadActive reads the saved selection through the caller's database snapshot.
// It does not import files or open another connection.
func ReadActive(ctx context.Context, q statewrite.Reader, harness string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT account FROM account_active WHERE harness = ?", harness).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SetActive records that harness uses account id, or none when id is empty.
func (s *Store) SetActive(harness, id string) error {
	if !validActiveID(id) {
		return errors.New("An account selection needs a saved account ID.")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx := context.Background()
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return setActiveRow(ctx, conn, harness, id) })
}
