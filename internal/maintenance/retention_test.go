package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
)

func TestArchivedRunRetention(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	var removedBytes int64
	for _, tc := range []struct {
		id, status string
		archived   bool
		changed    time.Time
	}{
		{"wf_old", "ok", true, old},
		{"wf_new", "ok", true, now.Add(-time.Hour)},
		{"wf_live", "ok", false, old},
		{"wf_running", "running", true, old},
		{"wf_queued", "queued", true, old},
	} {
		root := runstore.RunsDir()
		if tc.archived {
			root = runstore.ArchiveDir()
		}
		dir := filepath.Join(root, tc.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(runstore.Meta{ID: tc.id, Status: tc.status, EndedAt: old})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "output"), []byte("result"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Join(dir, "meta.json"), filepath.Join(dir, "output"), dir} {
			if err := os.Chtimes(path, tc.changed, tc.changed); err != nil {
				t.Fatal(err)
			}
		}
		if tc.id == "wf_old" {
			removedBytes = runstore.DirSize(dir)
		}
	}
	var logs []string
	result, err := Run(context.Background(), Options{Now: now, Repos: []string{}, Logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wf_old", "wf_new", "wf_live", "wf_running", "wf_queued"} {
		_, err := os.Stat(runstore.RunDir(id))
		if id == "wf_old" {
			if !os.IsNotExist(err) {
				t.Errorf("old archived run remains: %v", err)
			}
		} else if err != nil {
			t.Errorf("%s was not kept: %v", id, err)
		}
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "wf_old: age 720h0m0s, freed 113 bytes") {
		t.Errorf("deletion logs: %v", logs)
	}
	if result.BytesFreed != removedBytes {
		t.Errorf("freed %d bytes, want %d", result.BytesFreed, removedBytes)
	}
}

func TestArchivedRunRetentionLastChange(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, recent string
		retention    time.Duration
		keep         bool
	}{
		{"expired", "", 14 * 24 * time.Hour, false},
		{"root changed", ".", 14 * 24 * time.Hour, true},
		{"file changed", "nested/output", 14 * 24 * time.Hour, true},
		{"nested directory changed", "nested", 14 * 24 * time.Hour, true},
		{"ended recently", "ended", 14 * 24 * time.Hour, true},
		{"at boundary", "boundary", 14 * 24 * time.Hour, true},
		{"disabled", "", 0, true},
		{"live shadows archive", "live", 14 * 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("DYNA_ARCHIVE_DIR", "")
			dir := filepath.Join(runstore.ArchiveDir(), "wf_age")
			if err := os.MkdirAll(filepath.Join(dir, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			ended := now.Add(-30 * 24 * time.Hour)
			if tc.recent == "ended" {
				ended = now.Add(-time.Hour)
			}
			b, err := json.Marshal(runstore.Meta{ID: "wf_age", Status: "ok", EndedAt: ended})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "nested", "output"), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			old := now.Add(-30 * 24 * time.Hour)
			if err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				return os.Chtimes(path, old, old)
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(runstore.RunsDir(), 0700); err != nil {
				t.Fatal(err)
			}
			live := filepath.Join(runstore.RunsDir(), "wf_age")
			if tc.recent == "live" {
				if err := os.Mkdir(live, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(dir, live); err != nil {
				t.Fatal(err)
			}
			switch tc.recent {
			case "", "ended", "live":
			case "boundary":
				changed := now.Add(-tc.retention)
				if err := os.Chtimes(dir, changed, changed); err != nil {
					t.Fatal(err)
				}
			default:
				changed := now.Add(-time.Hour)
				if err := os.Chtimes(filepath.Join(dir, tc.recent), changed, changed); err != nil {
					t.Fatal(err)
				}
			}
			wantBytes := runstore.DirSize(dir)
			got, err := sweepArchivedRuns(context.Background(), now, tc.retention, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(dir)
			if tc.keep {
				if err != nil || got != (Pruned{}) {
					t.Fatalf("kept archive: stat=%v freed=%+v", err, got)
				}
			} else {
				if !os.IsNotExist(err) || got != (Pruned{Items: 1, Bytes: wantBytes}) {
					t.Fatalf("expired archive: stat=%v freed=%+v want 1 item of %d bytes", err, got, wantBytes)
				}
				if _, err := os.Lstat(live); !os.IsNotExist(err) {
					t.Fatalf("archive pointer remains: %v", err)
				}
			}
		})
	}
}

func TestClosedSessionLogRetention(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	type savedFile struct {
		path   string
		data   string
		remove bool
	}
	var files []savedFile
	var wantFreed int64
	for i, tc := range []struct {
		name, status   string
		closed         time.Time
		locked, remove bool
	}{
		{"expired", "ok", now.Add(-8 * 24 * time.Hour), false, true},
		{"recent", "ok", now.Add(-23 * time.Hour), false, false},
		{"boundary", "ok", now.Add(-sessionLogRetention), false, false},
		{"open", "ok", time.Time{}, false, false},
		{"running", "running", now.Add(-8 * 24 * time.Hour), false, false},
		{"queued", "queued", now.Add(-8 * 24 * time.Hour), false, false},
		{"locked", "ok", now.Add(-8 * 24 * time.Hour), true, false},
		{"metadata-locked", "ok", now.Add(-8 * 24 * time.Hour), true, false},
		{"legacy-done", "ok", now.Add(-8 * 24 * time.Hour), false, false},
		{"unknown-status", "unknown", now.Add(-8 * 24 * time.Hour), false, false},
	} {
		id := fmt.Sprintf("ses_20261001-000000-%08d", i)
		dir := filepath.Join(runstore.SessionsDir(), id)
		meta, _ := json.Marshal(map[string]any{"id": id, "closed": tc.closed, "turns": 1, "done": tc.name == "legacy-done"})
		recordtest.WriteRaw(t, runstore.SessionsDir(), id, meta)
		files = append(files, savedFile{recordtest.Path(t, runstore.SessionsDir(), id), string(meta), false})
		turn, _ := json.Marshal(map[string]any{"n": 1, "status": tc.status})
		for path, data := range map[string]string{
			"turns/1.json":        string(turn),
			"agents/1/stdout.log": "raw-log", "agents/1/output.jsonl": "normalized-log",
		} {
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			remove := tc.remove && path == "agents/1/stdout.log"
			files = append(files, savedFile{full, data, remove})
			if remove {
				wantFreed += int64(len(data))
			}
		}
		if tc.locked {
			name := "turn.lock"
			if tc.name == "metadata-locked" {
				name = "session.lock"
			}
			unlock, owned, err := store.TryLock(filepath.Join(dir, name))
			if err != nil || !owned {
				t.Fatalf("lock: %t %v", owned, err)
			}
			defer unlock()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The first pass at this retention age only measures.
	if _, err := Run(ctx, Options{Now: now, Repos: []string{}}); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, Options{Now: now, Repos: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		got, err := os.ReadFile(file.path)
		if file.remove {
			if !os.IsNotExist(err) {
				t.Errorf("expired log remains: %s (%v)", file.path, err)
			}
		} else if err != nil || string(got) != file.data {
			t.Errorf("retained file changed: %s: %q %v", file.path, got, err)
		}
	}
	if result.BytesFreed != wantFreed {
		t.Errorf("freed %d bytes, want %d", result.BytesFreed, wantFreed)
	}
}

func TestArchivedRunRetentionProtectsReadOnlyRecoveryRecords(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	dir := filepath.Join(runstore.ArchiveDir(), "wf_frozen_recovery")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(runstore.Meta{ID: "wf_frozen_recovery", Status: "ok", EndedAt: old})
	path := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(path, meta, 0400); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "journal.jsonl")
	if err := os.WriteFile(journal, []byte("recovery evidence\n"), 0400); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, journal, dir} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	var logs []string
	for pass := 0; pass < 2; pass++ {
		freed, err := sweepArchivedRuns(t.Context(), now, 14*24*time.Hour, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
		if err != nil || freed != (Pruned{}) {
			t.Fatalf("pass %d: freed=%+v error=%v", pass, freed, err)
		}
		if len(logs) != 1 || !strings.Contains(logs[0], "protected read-only archived run wf_frozen_recovery") {
			t.Fatalf("pass %d logs=%v", pass, logs)
		}
	}
	for _, p := range []string{dir, path, journal} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := fs.FileMode(0400)
		if p == dir {
			want = 0500
		}
		if info.Mode().Perm() != want || !info.ModTime().Equal(old) {
			t.Fatalf("protected metadata changed: %s mode=%o mtime=%s", p, info.Mode().Perm(), info.ModTime())
		}
	}
	got, err := os.ReadFile(journal)
	if err != nil || string(got) != "recovery evidence\n" {
		t.Fatalf("protected evidence changed: %q %v", got, err)
	}
}
