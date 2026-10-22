package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

// Packs go 7 days after their PR closed or merged; open PRs keep theirs, and a
// pack with no record goes when its newest file is that old.
func TestReviewInputsGoSevenDaysAfterTheirPRClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prs")
	// The real clock: state.Write drops closed records older than 14 days.
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	records := map[string]*state.Record{
		"open":          {Key: "open", State: "open"},
		"merged-8-days": {Key: "merged-8-days", State: "merged", ClosedAt: now.Add(-8 * 24 * time.Hour)},
		"closed-6-days": {Key: "closed-6-days", State: "closed", ClosedAt: now.Add(-6 * 24 * time.Hour)},
		// Reopened and closed again: ClosedAt keeps the first close.
		"reclosed": {Key: "reclosed", State: "closed", ClosedAt: now.Add(-8 * 24 * time.Hour)},
	}
	if err := state.Write(root, state.State{Version: 1, Records: records}); err != nil {
		t.Fatal(err)
	}
	pack := func(dir string, changed time.Time) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "head-1.patch")
		if err := os.WriteFile(file, []byte("diff"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{file, dir} {
			if err := os.Chtimes(p, changed, changed); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	keep := []string{
		pack(state.ReviewInputsDir(root, "open"), old),
		pack(state.ReviewInputsDir(root, "closed-6-days"), old),
		pack(state.ReviewInputsDir(root, "reclosed"), now.Add(-24*time.Hour)),
		pack(filepath.Join(root, "review-inputs", "orphan-recent"), now.Add(-time.Hour)),
	}
	remove := []string{
		pack(state.ReviewInputsDir(root, "merged-8-days"), old),
		pack(filepath.Join(root, "review-inputs", "orphan-old"), old),
	}
	dry, err := sweepReviewInputs(context.Background(), root, now, true)
	if err != nil || dry != (Pruned{Items: 2, Bytes: 8}) {
		t.Fatalf("dry pass = %+v, %v; want 2 packs of 4 bytes measured", dry, err)
	}
	for _, dir := range append(keep, remove...) {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("dry pass removed %s: %v", dir, err)
		}
	}
	freed, err := sweepReviewInputs(context.Background(), root, now, false)
	if err != nil || freed != (Pruned{Items: 2, Bytes: 8}) {
		t.Fatalf("freed %+v, %v; want 2 packs of 4 bytes", freed, err)
	}
	for _, dir := range keep {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("kept pack removed: %s: %v", dir, err)
		}
	}
	for _, dir := range remove {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("expired pack remains: %s: %v", dir, err)
		}
	}
}
