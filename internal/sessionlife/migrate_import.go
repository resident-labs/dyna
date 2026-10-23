package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
)

func migrateSessionImport(db *sql.DB, sessions string) error {
	var version string
	err := db.QueryRow("SELECT value FROM meta WHERE key='sessions.import'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version == "" || version == "0" {
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before importing sessions: %w", err)
		}
	}
	return database.Migrate(db, "sessions.import", 1, func(conn *sql.Conn, _ int) error {
		return importSessionRecords(conn, sessions)
	})
}

// TODO: legacy/shim remove later — import retained session.json records once
// before the engine serves calls. Canonical rows always win.
func importSessionRecords(conn *sql.Conn, sessions string) error {
	entries, err := os.ReadDir(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		source := filepath.Join(sessions, id, legacyName)
		if err := checkID(id); err != nil {
			return fmt.Errorf("import session %s: %w", source, err)
		}
		var exists int
		err := conn.QueryRowContext(context.Background(), "SELECT 1 FROM session_records WHERE id=?", id).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("import session %s: %w: session folder is a symlink", source, ErrRecord)
		}
		if !entry.IsDir() {
			continue
		}
		raw, _, err := legacyLoadRaw(sessions, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			var record struct {
				ID string `json:"id"`
			}
			err = json.Unmarshal(raw, &record)
			if err == nil && record.ID != id {
				err = fmt.Errorf("%w: record ID %q does not match folder %q", ErrRecord, record.ID, id)
			}
		}
		if err == nil {
			err = writeRow(conn, id, raw, "ON CONFLICT(id) DO NOTHING")
		}
		if err != nil {
			return fmt.Errorf("import session %s: %w", source, err)
		}
	}
	return nil
}
