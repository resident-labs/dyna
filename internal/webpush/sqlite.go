package webpush

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
)

const schema = `CREATE TABLE IF NOT EXISTS push_subscriptions (
 device_id TEXT PRIMARY KEY, endpoint TEXT NOT NULL, p256dh TEXT NOT NULL,
 auth TEXT NOT NULL, names INTEGER NOT NULL, created TEXT NOT NULL
);`

const entryColumns = "device_id,endpoint,p256dh,auth,names,created"

func openDB(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	// The copy's stamp lives in the shared table, which the version 2 step
	// fills from the old meta row.
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "webpush.version", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — moves the stamp that #1483 kept in meta into state_mirrors; remove with the copy, when requirement 31 ends.
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT ?, value FROM meta WHERE key = 'webpush.mirror' AND value != 'absent'`, mirrorKey)
		return err
	})
	return db, err
}

// mirrorKey names the subscription copy in state_mirrors.
const mirrorKey = "webpush/subscriptions"

// Migrate creates the subscription table and imports the old file at engine start.
func Migrate(dataDir string) error {
	db, err := openDB(dataDir)
	if err != nil {
		return err
	}
	return importLegacy(db, legacyDir(dataDir))
}

func scanEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var created string
	if err := row.Scan(&e.DeviceID, &e.Endpoint, &e.P256dh, &e.Auth, &e.Names, &created); err != nil {
		return Entry{}, err
	}
	var err error
	e.Created, err = time.Parse(time.RFC3339Nano, created)
	return e, err
}

func saveEntry(conn *sql.Conn, e Entry) error {
	_, err := conn.ExecContext(context.Background(), `INSERT INTO push_subscriptions (`+entryColumns+`)
 VALUES(?,?,?,?,?,?) ON CONFLICT(device_id) DO UPDATE SET endpoint=excluded.endpoint,
 p256dh=excluded.p256dh, auth=excluded.auth, names=excluded.names, created=excluded.created`,
		e.DeviceID, e.Endpoint, e.P256dh, e.Auth, e.Names, e.Created.Format(time.RFC3339Nano))
	return err
}

// TODO: legacy/shim remove later — old releases store subscriptions under connect/push; remove when requirement 31 ends.
func legacyDir(dataDir string) string { return filepath.Join(dataDir, "connect", "push") }

// TODO: legacy/shim remove later — reads the subscription file when an old binary wrote it; remove when requirement 31 ends.
func importLegacy(db *sql.DB, dir string) error {
	_, err := state.For(db).ImportIfForeign(context.Background(), mirrorKey, filepath.Join(dir, subsFile), func(conn *sql.Conn, data []byte) error {
		// A damaged file is skipped once; the rows stay.
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("webpush: subscription file is damaged: %w", state.ErrSkipForeign)
		}
		if _, err := conn.ExecContext(context.Background(), "DELETE FROM push_subscriptions"); err != nil {
			return err
		}
		for _, e := range entries {
			if err := saveEntry(conn, e); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// TODO: legacy/shim remove later — write-only rollback copy for the previous release; remove when requirement 31 ends.
func (s *Store) mirror() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return state.For(s.db).Mirror(context.Background(), mirrorKey, filepath.Join(s.dir, subsFile), func(out io.Writer) error {
		entries, err := s.List()
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	})
}
