package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	stepsForTest := map[int][]string{schemaVersion - 1: {"CREATE TABLE probe (id INTEGER PRIMARY KEY)"}}
	before := func(version int) error {
		_, err := backup(ctx, db, dir, fmt.Sprintf("pre-upgrade-v%d-", version), "pre-upgrade-v*-*.db", time.Now())
		return err
	}
	// A failed backup must leave the old version untouched.
	backupDir := filepath.Join(dir, "backups")
	if err := os.WriteFile(backupDir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", stepsForTest, before); err == nil {
		t.Fatal("upgrade succeeded without backup")
	}
	var value string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil || value != strconv.Itoa(schemaVersion-1) {
		t.Fatalf("schema_version after failed backup = %q, %v", value, err)
	}
	if err := os.Remove(backupDir); err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", stepsForTest, before); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(backupDir, "pre-upgrade-v"+strconv.Itoa(schemaVersion-1)+"-*.db"))
	if err != nil || len(names) != 1 {
		t.Fatalf("backups: %v, %v", names, err)
	}
	info, err := os.Stat(names[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	// The backup must hold the old schema without the probe table, so it was taken before the step ran.
	backupDB, err := sql.Open("sqlite", names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	if err := backupDB.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil || value != strconv.Itoa(schemaVersion-1) {
		t.Fatalf("backup version %q: %v", value, err)
	}
	var probes int
	if err := backupDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'probe'").Scan(&probes); err != nil || probes != 0 {
		t.Fatalf("backup has probe table: %d, %v", probes, err)
	}
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil || value != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version after upgrade = %q, %v", value, err)
	}
}
