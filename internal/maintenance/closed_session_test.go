package maintenance_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
)

// Sessions closed more than 30 days ago lose their folder and record, unless
// they still matter. The first pass at that age only measures.
func TestClosedSessionsGoAfterThirtyDays(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	sessions := runstore.SessionsDir()
	session := func(id string, closed time.Time) {
		if err := os.MkdirAll(filepath.Join(sessions, id, "agents", "1"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessions, id, "agents", "1", "output.jsonl"), []byte("transcript"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := map[string]any{"id": id}
		if !closed.IsZero() {
			rec["closed"] = closed
		}
		recordtest.Write(t, sessions, id, rec)
	}
	old, recent := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	session("ses_old", old)
	session("ses_recent", recent)
	session("ses_open", time.Time{})
	session("ses_kept", old)
	session("ses_busy", old)
	unlock, owned, err := store.TryLock(filepath.Join(sessions, "ses_busy", "session.lock"))
	if err != nil || !owned {
		t.Fatalf("lock ses_busy: %v, %v", owned, err)
	}
	defer unlock()
	// keep takes other stores' locks, so the sweep must not hold the
	// session's own lock while it asks.
	keep := func(_ context.Context, id string) (bool, error) {
		if id != "ses_busy" {
			release, owned, err := store.TryLock(filepath.Join(sessions, id, "session.lock"))
			if err != nil || !owned {
				t.Errorf("keep(%s) ran while the sweep held the session lock: %v", id, err)
			} else {
				release()
			}
		}
		return id == "ses_kept", nil
	}
	run := func() maintenance.Result {
		result, err := maintenance.Run(context.Background(), maintenance.Options{Now: now, Repos: []string{}, ClosedSessionKeeper: func(context.Context) (func(context.Context, string) (bool, error), error) { return keep, nil }})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(sessions, id))
		_, _, recErr := sessionlife.LoadRaw(sessions, id)
		if (err == nil) != (recErr == nil) {
			t.Fatalf("%s: folder %v, record %v; want both or neither", id, err, recErr)
		}
		return err == nil
	}
	dry := run()
	if p := dry.Pruned["closed sessions (dry run)"]; p.Items != 1 || p.Bytes == 0 {
		t.Fatalf("dry pass = %+v, want 1 session measured", dry.Pruned)
	}
	if !exists("ses_old") {
		t.Fatal("the dry pass removed a session")
	}
	real := run()
	if p := real.Pruned["closed sessions"]; p.Items != 1 {
		t.Fatalf("second pass = %+v, want 1 session removed", real.Pruned)
	}
	if exists("ses_old") {
		t.Fatal("ses_old was closed 31 days ago and kept")
	}
	for _, id := range []string{"ses_recent", "ses_open", "ses_kept", "ses_busy"} {
		if !exists(id) {
			t.Fatalf("%s was removed", id)
		}
	}
}
