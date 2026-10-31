package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const backupStamp = "20060102T150405.000000000Z"

// Backup writes a consistent online copy and retains the newest three copies.
func (s *Store) Backup(ctx context.Context, now time.Time) (string, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	var dbPath string
	if err := s.read.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return "", err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return "", err
	}
	name := filepath.Join(dir, "store-"+now.UTC().Format(backupStamp)+".db")
	partial := name + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	defer os.Remove(partial)
	if _, err := s.read.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		return "", err
	}
	if err := os.Rename(partial, name); err != nil {
		return "", err
	}
	folder, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	if err := folder.Sync(); err != nil {
		return "", err
	}
	names, err := filepath.Glob(filepath.Join(dir, "store-*.db"))
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	if len(names) > 3 {
		for _, old := range names[:len(names)-3] {
			if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		if err := folder.Sync(); err != nil {
			return "", err
		}
	}
	return name, nil
}

// BackupCopyStatus describes the most recent remote copy result.
type BackupCopyStatus struct {
	LastSuccess time.Time
	Failure     string
}

// RecordBackupCopy records a successful copy or its failure, preserving the last success.
func (s *Store) RecordBackupCopy(ctx context.Context, at time.Time, failure string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if failure == "" {
			_, err := tx.ExecContext(ctx, "INSERT INTO backup_copy_status(id,last_success,failure) VALUES(1,?,'') ON CONFLICT(id) DO UPDATE SET last_success=excluded.last_success,failure=''", at.UnixMilli())
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO backup_copy_status(id,last_success,failure) VALUES(1,NULL,?) ON CONFLICT(id) DO UPDATE SET failure=excluded.failure", failure)
		return err
	})
}

// BackupCopyStatus reads the persisted remote copy result. An empty store returns zero values.
func (s *Store) BackupCopyStatus(ctx context.Context) (BackupCopyStatus, error) {
	var status BackupCopyStatus
	var success sql.NullInt64
	err := s.read.QueryRowContext(ctx, "SELECT last_success,failure FROM backup_copy_status WHERE id=1").Scan(&success, &status.Failure)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if success.Valid {
		status.LastSuccess = time.UnixMilli(success.Int64).UTC()
	}
	return status, err
}
