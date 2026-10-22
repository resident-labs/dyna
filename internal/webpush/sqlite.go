package webpush

import (
	"context"
	"database/sql"
	"encoding/json"
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
	err = database.Migrate(db, "webpush.version", 3, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		if old < 2 {
			// Preserve the stamp from version 1, including an owned empty set.
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT ?, value FROM meta WHERE key = 'webpush.mirror' AND value != 'absent'`, mirrorKey); err != nil {
				return err
			}
		}
		return importLegacy(conn, legacyDir(dataDir))

	})
	return db, err
}

// mirrorKey names the subscription copy in state_mirrors.
const mirrorKey = "webpush/subscriptions"

// Migrate prepares subscriptions and imports retained state once.
func Migrate(dataDir string) error {
	_, err := openDB(dataDir)
	return err
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

// TODO: legacy/shim remove later — imports retained push.json during schema upgrade;
// remove when all supported data directories have passed webpush.version 3.
func importLegacy(conn *sql.Conn, dir string) error {
	_, err := state.ImportMigration(context.Background(), conn, mirrorKey, filepath.Join(dir, subsFile), func(conn *sql.Conn, data []byte) error {
		var entries []Entry
		// Damaged retained data must not block startup or replace canonical rows.
		if json.Unmarshal(data, &entries) != nil {
			return nil
		}
		for _, e := range entries {
			if _, err := conn.ExecContext(context.Background(), `INSERT OR IGNORE INTO push_subscriptions (`+entryColumns+`)
VALUES(?,?,?,?,?,?)`, e.DeviceID, e.Endpoint, e.P256dh, e.Auth, e.Names, e.Created.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
