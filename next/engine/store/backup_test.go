package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupAndCopyStatus(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordBackupCopy(ctx, at, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBackupCopy(ctx, at.Add(time.Hour), "offline"); err != nil {
		t.Fatal(err)
	}
	status, err := s.BackupCopyStatus(ctx)
	if err != nil || !status.LastSuccess.Equal(at) || status.Failure != "offline" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	var path string
	for i := 0; i < 4; i++ {
		path, err = s.Backup(ctx, at.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	names, err := filepath.Glob(filepath.Join(filepath.Dir(path), "store-*.db"))
	if err != nil || len(names) != 3 {
		t.Fatalf("backups = %v, %v", names, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var failure string
	if err := db.QueryRow("SELECT failure FROM backup_copy_status WHERE id=1").Scan(&failure); err != nil || failure != "offline" {
		t.Fatalf("backup failure = %q, %v", failure, err)
	}
}

func TestUpgradeBackup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("DROP INDEX events_client_id; ALTER TABLE events DROP COLUMN client_id; ALTER TABLE sessions DROP COLUMN close_reason; ALTER TABLE boxes ADD COLUMN parent TEXT REFERENCES boxes(id); DROP TABLE host_rooms; DROP TABLE device_scopes; DROP TABLE devices; DROP TABLE pairing_codes; DROP TABLE backup_copy_status; ALTER TABLE pr_owners DROP COLUMN branch; UPDATE meta SET value='27' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A failed backup must leave the old version untouched.
	backupDir := filepath.Join(dir, "backups")
	if err := os.WriteFile(backupDir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("upgrade succeeded without backup")
	}
	if err := os.Remove(backupDir); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	names, err := filepath.Glob(filepath.Join(backupDir, "pre-upgrade-v27-*.db"))
	if err != nil || len(names) != 1 {
		t.Fatalf("backups: %v, %v", names, err)
	}
	info, err := os.Stat(names[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	db, err := sql.Open("sqlite", names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version); err != nil || version != "27" {
		t.Fatalf("backup version %q: %v", version, err)
	}
}
