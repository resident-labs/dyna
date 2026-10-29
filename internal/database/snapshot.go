package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot writes one transactionally consistent copy of db. VACUUM INTO reads
// a single snapshot while other connections continue to write. The destination
// is published with a hard link, which never replaces an existing path.
func Snapshot(ctx context.Context, db *sql.DB, destination string) (string, error) {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot destination: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("snapshot destination already exists: %s", absolute)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("check snapshot destination: %w", err)
	}

	// Stage beside the destination so os.Link publishes on one filesystem. The
	// private folder and empty 0600 file keep the snapshot private from creation;
	// SQLite's VACUUM INTO accepts an existing empty file.
	staged, err := os.MkdirTemp(filepath.Dir(absolute), ".dyna-snapshot-")
	if err != nil {
		return "", fmt.Errorf("prepare state snapshot: %w", err)
	}
	defer os.RemoveAll(staged)
	file, err := os.CreateTemp(staged, "snapshot-*.db")
	if err != nil {
		return "", fmt.Errorf("create state snapshot: %w", err)
	}
	partial := file.Name()
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("create state snapshot: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		return "", fmt.Errorf("copy Dyna state: %w", err)
	}
	if err := os.Link(partial, absolute); errors.Is(err, os.ErrExist) {
		// A competing file appeared after the first check. Leave it untouched.
		return "", fmt.Errorf("snapshot destination already exists: %s", absolute)
	} else if err != nil {
		return "", fmt.Errorf("publish state snapshot: %w", err)
	}
	return absolute, nil
}
