package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
)

// closedSessionWithStdout writes a session closed at closed, holding a raw
// stdout of n bytes, and returns the stdout path.
func closedSessionWithStdout(t *testing.T, id string, closed time.Time, n int) string {
	t.Helper()
	dir := filepath.Join(runstore.SessionsDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"id": id, "closed": closed.UTC(), "turns": 0}
	if err := sessionlife.Save(runstore.SessionsDir(), id, meta); err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(dir, "agents", "1", "stdout.log")
	if err := os.WriteFile(stdout, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	return stdout
}

// Raw stdout of a closed session goes one day after close, not seven.
func TestClosedSessionStdoutGoesAfterOneDay(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	recent := closedSessionWithStdout(t, "ses_20261019-130000-aaaaaaaa", now.Add(-23*time.Hour), 10)
	old := closedSessionWithStdout(t, "ses_20261019-110000-bbbbbbbb", now.Add(-25*time.Hour), 20)
	freed, err := sweepClosedSessionLogs(context.Background(), now, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if freed != (Pruned{Items: 1, Bytes: 20}) {
		t.Fatalf("freed %+v, want the stdout closed 25 hours ago", freed)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("stdout closed 23 hours ago was removed: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stdout closed 25 hours ago remains: %v", err)
	}
}

// The first pass at a new age only measures and reports; the next one removes.
func TestFirstPassAtANewAgeOnlyMeasures(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	stdout := closedSessionWithStdout(t, "ses_20261019-110000-cccccccc", now.Add(-48*time.Hour), 30)
	pass := func() Result {
		t.Helper()
		result := Result{Kept: map[string]int{}, Pruned: map[string]Pruned{}}
		if err := sweepWithDryPass(context.Background(), &result, "closed session stdout", sessionLogRetention, func(dry bool) (Pruned, error) {
			return sweepClosedSessionLogs(context.Background(), now, dry, nil)
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := pass()
	if first.Pruned["closed session stdout (dry run)"] != (Pruned{Items: 1, Bytes: 30}) || first.BytesFreed != 0 {
		t.Fatalf("first pass = %+v, want a dry run that measures 30 bytes and frees nothing", first)
	}
	if _, err := os.Stat(stdout); err != nil {
		t.Fatalf("dry pass removed stdout: %v", err)
	}
	second := pass()
	if second.Pruned["closed session stdout"] != (Pruned{Items: 1, Bytes: 30}) || second.BytesFreed != 30 {
		t.Fatalf("second pass = %+v, want it to free 30 bytes", second)
	}
	if _, err := os.Stat(stdout); !os.IsNotExist(err) {
		t.Fatalf("stdout remains after the real pass: %v", err)
	}
}
