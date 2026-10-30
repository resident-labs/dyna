package subagent

import (
	"testing"
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
)

func TestShortTurnFlushesFinalActivity(t *testing.T) {
	e := newTestEnv(t)
	dir := store.DataDir()
	if err := activity.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := runstore.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: done")
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := activity.Read(db, activity.Key{Kind: activity.Turn, Owner: info.ID, Number: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != activity.CopierEnded || got.Signal.Copier != activity.CopierUnknown || got.Signal.CPU != activity.CPUUnsupported {
		t.Fatalf("activity = %+v", got)
	}
}
