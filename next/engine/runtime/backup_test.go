package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

type fakeBackupStore struct {
	dir     string
	calls   int
	failure string
}

func (s *fakeBackupStore) Backup(_ context.Context, _ time.Time) (string, error) {
	s.calls++
	path := filepath.Join(s.dir, "source")
	return path, os.WriteFile(path, []byte("private database"), 0600)
}
func (s *fakeBackupStore) RecordBackupCopy(_ context.Context, _ time.Time, failure string) error {
	s.failure = failure
	return nil
}

func TestBackupFlags(t *testing.T) {
	for _, cfg := range []Config{{BackupDest: "host"}, {BackupRecipient: "bad"}, {BackupDest: "host", BackupRecipient: "bad"}} {
		if err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "backup.") {
			t.Fatalf("invalid flags accepted: %v", err)
		}
	}
}

func TestBackupStartAge(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{23 * time.Hour, 25 * time.Hour} {
		dir := t.TempDir()
		name := "store-" + now.Add(-age).Format(backupStamp) + ".db"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		st := &fakeBackupStore{dir: dir}
		b := backupLoop{store: st, dir: dir, now: func() time.Time { return now }}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b.start(ctx, nil)
		if (st.calls == 1) != (age > 24*time.Hour) {
			t.Fatalf("age %v: calls %d", age, st.calls)
		}
	}
}

func TestBackupCopyFailure(t *testing.T) {
	dir := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := &fakeBackupStore{dir: dir}
	b := backupLoop{store: st, dir: dir, dest: "backup-host", recipient: identity.Recipient(), now: func() time.Time { return now }, run: func(_ context.Context, dest, name string, size int64, input *os.File) error {
		data, err := io.ReadAll(input)
		if err != nil {
			t.Fatal(err)
		}
		if dest != "backup-host" || name != "dyna-20260102T030405000000000Z.db.age" || size != int64(len(data)) || strings.Contains(string(data), "private database") {
			t.Fatalf("bad copy command: %s %s %d", dest, name, size)
		}
		return errors.New("connection lost")
	}}
	b.backup(context.Background())
	if st.failure != "connection lost" {
		t.Fatalf("failure = %q", st.failure)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".encrypted-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files: %v %v", files, err)
	}
}
