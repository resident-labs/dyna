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
