package state

import (
	"path/filepath"
	"testing"
	"time"
)

// Write counts the closed records it removes; TakePruned returns the count
// once and resets it.
func TestTakePrunedCountsRemovedClosedRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prs")
	now := time.Now()
	st := State{Version: 1, Records: map[string]*Record{
		"open":       {Key: "open", State: "open"},
		"recent":     {Key: "recent", State: "merged", ClosedAt: now.Add(-24 * time.Hour)},
		"old-merged": {Key: "old-merged", State: "merged", ClosedAt: now.Add(-15 * 24 * time.Hour)},
		"old-closed": {Key: "old-closed", State: "closed", ClosedAt: now.Add(-20 * 24 * time.Hour)},
	}}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if n, err := TakePruned(root); err != nil || n != 2 {
		t.Fatalf("TakePruned = %d, %v; want the 2 old closed records", n, err)
	}
	if n, err := TakePruned(root); err != nil || n != 0 {
		t.Fatalf("second TakePruned = %d, %v; want 0 after reset", n, err)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || got.Records["open"] == nil || got.Records["recent"] == nil {
		t.Fatalf("records after prune = %v", got.Records)
	}
}
