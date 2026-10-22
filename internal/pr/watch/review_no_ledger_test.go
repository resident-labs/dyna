package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewAssignmentDoesNotOpenChecksDirectory(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	retired := filepath.Join(filepath.Dir(e.s.Store.Root), "checks")
	if err := os.WriteFile(retired, []byte("retired directory is unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := e.s.prepareReviewTask(context.Background(), r, "")
	if err != nil {
		t.Fatalf("review depended on retired check ledger: %v", err)
	}
	if strings.Contains(task, "dyna check") || strings.Contains(task, "exact-head check evidence") {
		t.Fatalf("review still requests local evidence: %s", task)
	}
}
