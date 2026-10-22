package state

import (
	"context"
	"database/sql"
	"os"

	"dyna-agent/internal/database"
)

// ImportMigration imports path on the connection held by a versioned store
// migration. It skips missing files and copies owned by an existing mirror
// stamp, including a rename pending when the process stopped. A fresh store
// needs no stamp table. The callback decides how to combine imported records
// with current rows. Errors abort the caller transaction. Source files and
// stamps stay unchanged; the store migration version prevents later imports.
func ImportMigration(ctx context.Context, conn *sql.Conn, key, path string, read func(*sql.Conn, []byte) error) (bool, error) {
	hasStamps, err := database.HasTable(ctx, conn, "state_mirrors")
	if err != nil {
		return false, err
	}
	var stamps stampQuerier
	if hasStamps {
		stamps = conn
	}
	info, err := importSource(ctx, stamps, key, path)
	if err != nil || info == nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := read(conn, raw); err != nil {
		return false, err
	}
	return true, nil
}

// importSource returns a regular file only when stamps does not own it.
// A nil stamps means the database has no stamp table.
func importSource(ctx context.Context, stamps stampQuerier, key, path string) (os.FileInfo, error) {
	info, err := regularFile(path)
	if err != nil || info == nil || stamps == nil {
		return info, err
	}
	stamp, err := readStamp(ctx, stamps, key)
	if err != nil || stamp.owns(info) {
		return nil, err
	}
	return info, nil
}
