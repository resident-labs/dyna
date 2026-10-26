package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/sessionlife/recordtest"
)

// Two closed sessions whose scratch is past its retention; the first sorts first.
const (
	protectedOwner = "ses_20260101-000000-aaaaaaaa"
	expiredOwner   = "ses_20260101-000000-zzzzzzzz"
)

func TestMergedPRCleanupSkipsProtectedScratch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("DYNA_SCRATCH_ROOT", filepath.Join(t.TempDir(), "scratch"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := time.Now().Add(-4 * 24 * time.Hour)
	closed := time.Now().Add(-scratch.ClosedRetention - time.Hour).UTC().Truncate(time.Second)
	for _, owner := range []string{protectedOwner, expiredOwner} {
		recordtest.WriteRaw(t, runstore.SessionsDir(), owner, []byte(`{"closed":"`+closed.Format(time.RFC3339)+`"}`))
		dir, err := scratch.Ensure(owner)
		if err != nil {
			t.Fatal(err)
		}
		note := filepath.Join(dir, "note")
		if err := os.WriteFile(note, []byte("evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{note, dir} {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	blocked := filepath.Join(scratch.Root(), protectedOwner)
	if err := os.Chmod(blocked, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
	store := &Store{Root: t.TempDir()}
	const key = "github.com/o/r#1"
	if err := store.update(func(st *State) error {
		st.Records[key] = &Record{Key: key, Author: "author", Watching: true, State: "open", Seen: map[string]string{}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var logs []string
	service := &Service{Store: store, CleanupMerged: func(ctx context.Context, _ Record, _ string) error {
		_, err := maintenance.Run(ctx, maintenance.Options{Repos: []string{}, Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }})
		return err
	}}
	snap := Snapshot{}
	snap.Pull.State = "closed"
	snap.Pull.Merged = true
	snap.Pull.Head.Ref = "feature"
	if err := service.process(t.Context(), key, snap, "", "", nil, nil, false); err != nil {
		t.Fatalf("scratch permission failure stopped merge cleanup: %v", err)
	}
	record, err := store.Get(key)
	if err != nil || record.State != "merged" {
		t.Fatalf("merge did not complete: %+v: %v", record, err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], blocked) {
		t.Fatalf("protected path log: %v", logs)
	}
	if _, err := os.Stat(filepath.Join(scratch.Root(), expiredOwner)); !os.IsNotExist(err) {
		t.Fatalf("cleanup stopped before next scratch owner: %v", err)
	}
}
