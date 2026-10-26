package maintenance_test

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/store/storetest"
)

// The pass removes notices that were delivered long ago, the largest field of a PR record.
func TestPassRemovesOldDeliveredPRNotices(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	record := &state.Record{Key: "k", State: "open", Notices: []state.Notice{
		{ID: "old", Done: true, DoneAt: time.Now().Add(-20 * 24 * time.Hour)},
		{ID: "pending"},
	}}
	if err := state.Write(state.Root(dataDir), state.State{Version: 1, Records: map[string]*state.Record{"k": record}}); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Pass(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Pruned["delivered PR notices"].Items; got != 1 {
		t.Fatalf("pass removed %d delivered notices, want 1", got)
	}
}
