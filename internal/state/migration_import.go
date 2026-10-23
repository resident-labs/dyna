package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

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

// ImportMigrationRoot imports a retained regular file inside root. It opens
// the source once, so path changes cannot redirect the read outside root.
// Like ImportMigration, it leaves both the source and mirror stamp unchanged.
func ImportMigrationRoot(ctx context.Context, conn *sql.Conn, key string, root *os.Root, path string, read func(*sql.Conn, []byte) error) (bool, error) {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("retained source %s is not a regular file", path)
	}
	hasStamps, err := database.HasTable(ctx, conn, "state_mirrors")
	if err != nil {
		return false, err
	}
	if hasStamps {
		stamp, err := readStamp(ctx, conn, key)
		if err != nil || stamp.owns(info) {
			return false, err
		}
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	if err := read(conn, raw); err != nil {
		return false, err
	}
	return true, nil
}
