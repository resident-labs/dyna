package worktrees

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

// benchSessions writes count session records near the live average meta size
// (about 26 KB), so one benchmark pass scans a realistic session table. Most
// records are closed and every record names a tree, like the live store.
func benchSessions(b *testing.B, count int) []string {
	b.Helper()
	b.Setenv("XDG_DATA_HOME", b.TempDir())
	tree := b.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("session-%04d", i)
		record := map[string]any{
			"id": id, "name": "worker", "roomId": "r1", "room": "dyna",
			"dir":     filepath.Join(tree, fmt.Sprintf("tree-%d", i%8)),
			"profile": "sonnet", "model": "model-x", "status": "ok",
			"created": base, "lastActivity": base, "turns": i,
		}
		if i%100 == 0 {
			record["service"] = true
		}
		if i%4 != 0 {
			record["closed"] = base
		}
		receipts := make(map[string]bool, 440)
		posts := make(map[string]bool, 560)
		for j := 0; j < 560; j++ {
			if j < 440 {
				receipts[fmt.Sprintf("msg_%04d_%03d", i, j)] = true
			}
			posts[fmt.Sprintf("post_%04d_%03d", i, j)] = true
		}
		record["inheritedReceipts"] = receipts
		record["closedPosts"] = posts
		recordtest.Write(b, runstore.SessionsDir(), id, record)
	}
	paths := make([]string, 8)
	for i := range paths {
		paths[i] = filepath.Join(tree, fmt.Sprintf("tree-%d", i))
	}
	return paths
}

// BenchmarkInferOwnersSessions measures one cold ownership inference pass
// over the whole session store, the cost every worktree cleanup pays.
func BenchmarkInferOwnersSessions(b *testing.B) {
	paths := benchSessions(b, 400)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := inferOwners(ctx, paths, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInferOwnersSessionsReused measures the pass over a reused
// runHistory, the shape a multi-block cleanup keeps between blocks.
func BenchmarkInferOwnersSessionsReused(b *testing.B) {
	paths := benchSessions(b, 400)
	ctx := context.Background()
	history := &runHistory{}
	if _, err := inferOwners(ctx, paths, history); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := inferOwners(ctx, paths, history); err != nil {
			b.Fatal(err)
		}
	}
}
