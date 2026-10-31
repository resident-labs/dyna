package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openBackup only accepts a standalone frozen backup, never a live WAL store.
// immutable prevents SQLite from creating sidecars or taking write locks.
func openBackup(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("backup is not a regular file")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_, err := os.Lstat(abs + suffix)
		if err == nil {
			return nil, fmt.Errorf("backup has SQLite sidecar %s; use a standalone backup", suffix)
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	for _, version := range []struct {
		key  string
		want int
	}{
		{"room.records", 1},
		{"room.records.import", 1},
		{"goals.schema", 5},
		{"goal_items.schema", 2},
		{"questions.schema", 4},
		{"room.issues.schema", 3},
		{"sessions.records", 7},
		{"sessions.import", 1},
		{"machines.version", 18},
		{"account.registry", 11},
		{"web_devices.version", 1},
		{"device_grants.version", 2},
		{"hooks.documents", 5},
		{"pr.state", 2},
		{"pr.record_parts", 1},
		{"localci.schema", 6},
		{"sessions.service", 1},
		{"sessions.history", 1},
	} {
		var got int
		if err := db.QueryRow("SELECT value FROM meta WHERE key=?", version.key).Scan(&got); err != nil {
			db.Close()
			return nil, fmt.Errorf("backup schema %s: %w", version.key, err)
		}
		if got != version.want {
			db.Close()
			return nil, fmt.Errorf("unsupported backup schema %s: got %d, want %d", version.key, got, version.want)
		}
	}
	return db, nil
}
