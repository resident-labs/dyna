package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// markCopy records the completion time in the staged copy, never in the source.
func markCopy(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS dyna_copy_marker (id INTEGER PRIMARY KEY CHECK (id = 1), copied_at TEXT NOT NULL);
INSERT OR REPLACE INTO dyna_copy_marker (id, copied_at) VALUES (1, ?)`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ClearCopyMarker makes a restored copy an ordinary database. It leaves an
// unmarked database unchanged.
func ClearCopyMarker(dataDir string) error {
	db, err := Open(dataDir)
	if err != nil {
		return err
	}
	_, err = db.Exec("DROP TABLE IF EXISTS dyna_copy_marker")
	return err
}

// CopyTime returns the time a database copy completed, or zero for an
// unmarked database. dataDir contains dyna.db.
func CopyTime(dataDir string) (time.Time, error) {
	db, err := Open(dataDir)
	if err != nil {
		return time.Time{}, err
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'dyna_copy_marker')").Scan(&exists); err != nil {
		return time.Time{}, err
	}
	if !exists {
		return time.Time{}, nil
	}
	var value string
	if err := db.QueryRow("SELECT copied_at FROM dyna_copy_marker WHERE id = 1").Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, fmt.Errorf("database copy marker has no timestamp: %w", err)
		}
		return time.Time{}, err
	}
	copied, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid database copy timestamp: %w", err)
	}
	return copied, nil
}
