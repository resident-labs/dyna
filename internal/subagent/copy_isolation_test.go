package subagent

import (
	"strings"
	"testing"
	"time"
)

func TestCopiedSessionCannotWake(t *testing.T) {
	e := newTestEnv(t)
	e.m.Spawner = closeLockSpawner{}
	info := completedFixtureSession(t, e)
	e.m.CopyTime = info.Created.Add(time.Nanosecond)
	if _, err := e.m.Wake(e.ctx, info.ID, WakeOptions{}); err == nil || !strings.Contains(err.Error(), "copied sessions") {
		t.Fatalf("wake copied session: %v", err)
	}
	e.m.CopyTime = info.Created.Add(-time.Nanosecond)
	if _, err := e.m.Wake(e.ctx, info.ID, WakeOptions{}); err != nil {
		t.Fatalf("wake new session: %v", err)
	}
}
