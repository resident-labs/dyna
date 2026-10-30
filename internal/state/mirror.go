package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// TODO: legacy/shim remove later — imports still read stamps from the old
// downgrade writer. Remove the table and stamp readers with the import side.
const mirrorSchema = `CREATE TABLE IF NOT EXISTS state_mirrors (
 key TEXT PRIMARY KEY,
 version TEXT NOT NULL,
 pending TEXT
)`

// MigrateMirrors creates the stamp table. Only the engine runs it, with its
// other stores (item 41), so a process that runs before the engine has
// migrated fails clearly instead of changing the schema.
func MigrateMirrors(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "state.mirrors", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), mirrorSchema)
		return err
	})
}

type mirrorStamp struct{ version, pending string }

// owns reports whether info is a copy that Mirror wrote. An empty version,
// on a platform that cannot tell rapid replacements apart, never matches.
func (s mirrorStamp) owns(info os.FileInfo) bool {
	version, id := store.VersionOf(info).Key(), fileID(info)
	return version != "" && version == s.version || id != "" && id == s.pending
}

type stampQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readStamp(ctx context.Context, q stampQuerier, key string) (mirrorStamp, error) {
	var s mirrorStamp
	var pending sql.NullString
	err := q.QueryRowContext(ctx, "SELECT version, pending FROM state_mirrors WHERE key = ?", key).Scan(&s.version, &pending)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	s.pending = pending.String
	return s, err
}

// fileID is info's device and inode, or empty where the platform has none.
func fileID(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}

// ErrSkipForeign marks a migration file that cannot be used. Wrap it to say why.
var ErrSkipForeign = errors.New("the file was skipped")

// regularFile is path's info, or nil when there is no file.
func regularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return info, nil
}

// ForgetMirrors drops the stamps of key and of every key under key + "/" in
// the caller's transaction, when a store deletes those records.
func ForgetMirrors(ctx context.Context, conn *sql.Conn, key string) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM state_mirrors WHERE key = ? OR substr(key, 1, ?) = ?", key, len(key)+1, key+"/")
	return err
}
