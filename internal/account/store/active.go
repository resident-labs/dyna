package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// The account each harness uses lives in dyna.db, beside the registry, so a
// removal deletes it with the account. Version 2 of the registry's
// migration creates the table.
const activeSchema = `CREATE TABLE IF NOT EXISTS account_active (
 harness TEXT PRIMARY KEY,
 account TEXT NOT NULL
);`

func (s *Store) activePath(harness string) string {
	return filepath.Join(s.Root, harness, ".active.json")
}

func activeKey(harness string) string { return "accounts/active/" + harness }

// activeLock is the harness's own lock for its selection. It is not
// accounts.lock, because a switch sets the selection while it holds that
// lock, and a second flock from the same process would wait on itself.
func (s *Store) activeLock(harness string) (func(), error) {
	if err := os.MkdirAll(filepath.Join(s.Root, harness), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Root, harness, ".active.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// validActiveID reports whether id is an account ID or empty (no account).
func validActiveID(id string) bool {
	if id == "" {
		return true
	}
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil
}

// importActive imports the harness's .active.json when another binary wrote
// it. A file this release cannot read is skipped once, and the row stays.
//
// TODO: legacy/shim remove later — the previous release reads and writes
// <harness>/.active.json; remove importActive and the copy in SetActive in
// requirement 31.
func (s *Store) importActive(ctx context.Context, db *sql.DB, harness string) error {
	w := statewrite.For(db)
	if foreign, err := w.Foreign(ctx, activeKey(harness), s.activePath(harness)); err != nil || !foreign {
		return err
	}
	unlock, err := s.activeLock(harness)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = w.ImportIfForeign(ctx, activeKey(harness), s.activePath(harness), func(conn *sql.Conn, raw []byte) error {
		var selection struct {
			ID string `json:"id"`
		}
		if len(raw) > 256 || json.Unmarshal(raw, &selection) != nil || !validActiveID(selection.ID) {
			return fmt.Errorf("account selection for %s: %w", harness, statewrite.ErrSkipForeign)
		}
		return setActiveRow(ctx, conn, harness, selection.ID)
	})
	return err
}

func setActiveRow(ctx context.Context, conn *sql.Conn, harness, id string) error {
	if id == "" {
		_, err := conn.ExecContext(ctx, "DELETE FROM account_active WHERE harness = ?", harness)
		return err
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO account_active(harness, account) VALUES (?, ?)
ON CONFLICT(harness) DO UPDATE SET account = excluded.account`, harness, id)
	return err
}

// forgetActive clears every harness selection of account id.
func forgetActive(ctx context.Context, conn *sql.Conn, id string) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM account_active WHERE account = ?", id)
	return err
}

// Active returns the ID of the account that harness uses, or empty.
func (s *Store) Active(harness string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := s.importActive(ctx, db, harness); err != nil {
		return "", err
	}
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

// SetActive records that harness uses account id, or none when id is empty,
// in the order state.Mirror needs: import, write the row, then copy it to
// the previous release's .active.json.
func (s *Store) SetActive(harness, id string) error {
	if !validActiveID(id) {
		return errors.New("An account selection needs a saved account ID.")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := s.importActive(ctx, db, harness); err != nil {
		return err
	}
	unlock, err := s.activeLock(harness)
	if err != nil {
		return err
	}
	defer unlock()
	w := statewrite.For(db)
	if err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return setActiveRow(ctx, conn, harness, id) }); err != nil {
		return err
	}
	// TODO: legacy/shim remove later — <harness>/.active.json is the previous
	// release's copy of the selection; remove with importActive.
	return w.Mirror(ctx, activeKey(harness), s.activePath(harness), func(out io.Writer) error {
		return json.NewEncoder(out).Encode(struct {
			ID string `json:"id"`
		}{id})
	})
}
