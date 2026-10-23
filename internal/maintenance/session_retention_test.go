package maintenance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestRetentionKeepsLogsWithoutCanonicalTerminalTurn(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)
	cases := []struct {
		id        string
		row, file string
		remove    bool
	}{
		{"ses_20261001-000000-aaaaaaaa", "", subagent.TurnOK, false},
		{"ses_20261001-000000-bbbbbbbb", subagent.TurnRunning, subagent.TurnOK, false},
		{"ses_20261001-000000-cccccccc", subagent.TurnOK, subagent.TurnRunning, true},
	}
	writeTurn := func(id string, status string) {
		t.Helper()
		path := filepath.Join(runstore.SessionsDir(), id, "turns", "1.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteJSON(path, subagent.Turn{N: 1, Status: status}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range cases {
		recordtest.Write(t, runstore.SessionsDir(), tc.id, subagent.Meta{ID: tc.id, Closed: old, Turns: 1})
		if tc.row != "" {
			writeTurn(tc.id, tc.row)
		}
	}
	// Import the two canonical turns before adding the stale completion file.
	if err := subagent.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	const stdout = "raw harness output"
	for _, tc := range cases {
		writeTurn(tc.id, tc.file)
		path := filepath.Join(runstore.SessionsDir(), tc.id, "agents", "1", "stdout.log")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(stdout), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dry := range []bool{true, false} {
		result, err := maintenance.Run(context.Background(), maintenance.Options{Now: now, Repos: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		kind := "closed session stdout"
		wantBytes := int64(len(stdout))
		if dry {
			kind += " (dry run)"
			wantBytes = 0
		}
		if result.Pruned[kind] != (maintenance.Pruned{Items: 1, Bytes: int64(len(stdout))}) || result.BytesFreed != wantBytes {
			t.Errorf("dry=%t: result = %+v, want only the canonical terminal turn's log", dry, result)
		}
		for _, tc := range cases {
			path := filepath.Join(runstore.SessionsDir(), tc.id, "agents", "1", "stdout.log")
			data, err := os.ReadFile(path)
			if tc.remove && !dry {
				if !os.IsNotExist(err) {
					t.Errorf("terminal turn log remains: %v", err)
				}
			} else if err != nil || string(data) != stdout {
				t.Errorf("dry=%t: kept log %s = %q, %v", dry, tc.id, data, err)
			}
		}
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
	records := map[string]string{}
	var wantFreed int64
	for i, tc := range []struct {
		name, status   string
		closed         time.Time
		locked, remove bool
	}{
		{"expired", "ok", now.Add(-8 * 24 * time.Hour), false, true},
		{"recent", "ok", now.Add(-23 * time.Hour), false, false},
		{"boundary", "ok", now.Add(-24 * time.Hour), false, false},
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
		fields := map[string]any{"id": id, "closed": tc.closed, "turns": 1}
		if tc.name == "legacy-done" {
			delete(fields, "closed")
			fields["finished"], fields["status"] = tc.closed, "dormant"
		}
		meta, _ := json.Marshal(fields)
		recordtest.WriteRaw(t, runstore.SessionsDir(), id, meta)
		records[id] = string(meta)
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
	if err := subagent.Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The first pass at this retention age only measures.
	if _, err := maintenance.Run(ctx, maintenance.Options{Now: now, Repos: []string{}}); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Run(ctx, maintenance.Options{Now: now, Repos: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range records {
		got, err := recordtest.ReadRaw(t, runstore.SessionsDir(), id)
		if err != nil || string(got) != want {
			t.Errorf("retained record changed: %s: %q %v", id, got, err)
		}
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
