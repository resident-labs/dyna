package subagent

import (
	"os"
	"path/filepath"
	"testing"

	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/store"
)

// Dormancy reads PR state under the publication lock, so a reviewer startup
// that keeps that lock would deadlock it. The lock must be free when the turn
// ends, and the dormancy write must then complete.
func TestDormancyRunsAfterAReviewerStartupReleasesTheLock(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	root := prstate.Root(filepath.Dir(e.m.Store.root()))
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, owned, err := store.TryLock(prstate.LockPath(root))
	if err != nil || !owned {
		t.Fatalf("reviewer startup retained the publication lock: owned=%v error=%v", owned, err)
	}
	unlock()
	if _, err := e.m.dormantAfterTurn(e.ctx, info.ID, 1); err != nil {
		t.Fatalf("dormancy after a reviewer startup: %v", err)
	}
	if got, err := e.m.Store.Info(info.ID); err != nil || got.Status != StatusDormant {
		t.Fatalf("session = %+v, %v; want dormant", got, err)
	}
}
