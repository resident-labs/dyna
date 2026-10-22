package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Rejected runner spool lines are kept a week for a bug report, then the
// maintenance pass removes them. Other spool files are never touched.
func TestSweepRejectedSpoolsRemovesOnlyOldRejects(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	files := map[string]time.Time{
		"old.rejected.jsonl":   now.Add(-rejectedSpoolRetention - time.Hour),
		"fresh.rejected.jsonl": now,
		"old.jsonl":            now.Add(-rejectedSpoolRetention - time.Hour),
	}
	for name, at := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	freed, err := sweepRejectedSpools(t.Context(), dir, now, false)
	if err != nil || freed.Items != 1 {
		t.Fatalf("freed %+v, %v; want the one old rejected file", freed, err)
	}
	for name := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if gone := os.IsNotExist(err); gone != (name == "old.rejected.jsonl") {
			t.Fatalf("%s: gone = %v", name, gone)
		}
	}
}
