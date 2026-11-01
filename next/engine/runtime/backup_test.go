package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type fakeBackupStore struct {
	dir        string
	calls      int
	failure    string
	alerts     int
	free, need int64
}

func (s *fakeBackupStore) AlertDisk(_ context.Context, host contract.HostName, free, need int64, _ time.Time) error {
	if host != contract.ControlPlane {
		return errors.New("alert on the wrong host")
	}
	s.alerts++
	s.free, s.need = free, need
	return nil
}

func plentyFree(context.Context) (int64, bool) { return 1 << 62, true }

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
		layout := box.Layout{Data: t.TempDir()}
		dir := layout.Backups()
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		name := "store-" + now.Add(-age).Format(backupStamp) + ".db"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		st := &fakeBackupStore{dir: dir}
		b := backupLoop{store: st, layout: layout, free: plentyFree, now: func() time.Time { return now }}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b.start(ctx)
		if (st.calls == 1) != (age > 24*time.Hour) {
			t.Fatalf("age %v: calls %d", age, st.calls)
		}
	}
}

func TestBackupWaitsForRoomAndClearsPartials(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	dir := layout.Backups()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.Store(), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"store-1.db.partial", ".encrypted-2.partial", "store-3.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	need := 2*1024 + 20*box.GiB
	free := need - 1
	st := &fakeBackupStore{dir: dir}
	b := backupLoop{store: st, layout: layout, recipient: identity.Recipient(), free: func(context.Context) (int64, bool) { return free, true }, now: time.Now, run: func(context.Context, string, int64, *os.File) error { return nil }}
	b.backup(context.Background())
	if st.calls != 0 || st.alerts != 1 || st.free != need-1 || st.need != need || st.failure != "low disk" {
		t.Fatalf("low disk: %+v", st)
	}
	names, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(names) != 1 || filepath.Base(names[0]) != "store-3.db" {
		t.Fatalf("left in backups: %v, %v", names, err)
	}
	free = need
	b.backup(context.Background())
	if st.calls != 1 || st.alerts != 1 || st.failure != "" {
		t.Fatalf("with room: %+v", st)
	}
}

// A broken probe reads as zero free. It must not stop backups for good, nor claim the disk is full.
func TestBackupRunsWhenDiskCannotBeMeasured(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	dir := layout.Backups()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	st := &fakeBackupStore{dir: dir}
	b := backupLoop{store: st, layout: layout, free: func(context.Context) (int64, bool) { return 0, false }, now: time.Now}
	b.backup(context.Background())
	if st.calls != 1 || st.alerts != 0 {
		t.Fatalf("calls %d alerts %d", st.calls, st.alerts)
	}
}

func TestBackupCopyFailure(t *testing.T) {
	layout := box.Layout{Data: t.TempDir()}
	dir := layout.Backups()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := &fakeBackupStore{dir: dir}
	for _, name := range []string{"AGENTS.md", "rooms/r/files/a", "rooms/r/transcripts/s/b", "archive/d", "connect/pending.json", "cliproxy/auth-v2/c"} {
		path := filepath.Join(layout.Data, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := backupLoop{store: st, layout: layout, dest: "backup-host", recipient: identity.Recipient(), free: plentyFree, now: func() time.Time { return now }, run: func(_ context.Context, dest string, size int64, input *os.File) error {
		data, err := io.ReadAll(input)
		if err != nil {
			t.Fatal(err)
		}
		if dest != "backup-host" || size != int64(len(data)) || strings.Contains(string(data), "private database") {
			t.Fatalf("bad copy command: %s %d", dest, size)
		}
		plain, err := age.Decrypt(bytes.NewReader(data), identity)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(plain)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		members := map[string]string{}
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			members[h.Name] = string(body)
		}
		for _, name := range []string{"AGENTS.md", "rooms/r/files/a", "rooms/r/transcripts/s/b", "archive/d", "connect/pending.json", "cliproxy/auth-v2/c"} {
			if members[name] != name {
				t.Fatalf("missing %s: %v", name, members)
			}
		}
		if members["store.db"] != "private database" {
			t.Fatalf("bad store: %v", members)
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
