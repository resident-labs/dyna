package worktrees

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

// Matching an owner's directory must not compare it with every tree: the
// engine has thousands of sessions and hundreds of trees on each pass.
func TestInferOwnersCostDoesNotGrowWithTrees(t *testing.T) {
	base := inferenceTestStore(t)
	for i := range 300 {
		id := fmt.Sprintf("session-%03d", i)
		recordtest.Write(t, runstore.SessionsDir(), id, map[string]any{"id": id, "dir": filepath.Join(base, "work", id)})
	}
	trees := make([]string, 300)
	for i := range trees {
		trees[i] = filepath.Join(base, "trees", fmt.Sprintf("tree-%03d", i))
	}
	allocs := func(paths []string) float64 {
		return testing.AllocsPerRun(2, func() {
			if _, err := inferOwners(context.Background(), paths, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	one, many := allocs(trees[:1]), allocs(trees)
	// Each extra tree costs one path resolve. Comparing every session with
	// every tree costs 300 x 299 more allocations and fails this bound.
	if many > 2*one {
		t.Fatalf("inference with %d trees made %.0f allocations, with 1 tree %.0f; want under %.0f", len(trees), many, one, 2*one)
	}
}
