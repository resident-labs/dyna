package maintenance_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
)

// The scheduled pass must delete old ended shell job rows, or the table grows
// without limit.
func TestPassDeletesOldEndedShellJobs(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	shs := &shells.Store{Root: filepath.Join(dataDir, "shells")}
	old := time.Now().Add(-4 * 24 * time.Hour)
	const oldID, runningID = "sh_20261022-120000-00000001", "sh_20261022-120000-00000002"
	if err := shs.Save(shells.Meta{ID: oldID, Cmd: []string{"true"}, Cwd: "/", Started: old}, &shells.Exit{Ended: old}); err != nil {
		t.Fatal(err)
	}
	if err := shs.Save(shells.Meta{ID: runningID, Cmd: []string{"sleep"}, Cwd: "/", Started: old}, nil); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Pass(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Pruned["shell job rows"].Items; got != 1 {
		t.Fatalf("pass removed %d shell job rows, want 1", got)
	}
	list, err := shs.List(true)
	if err != nil || len(list) != 1 || list[0].ID != runningID {
		t.Fatalf("jobs left = %v, %v; want only the running one", list, err)
	}
}
