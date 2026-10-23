package roomsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
)

// The run index reads dyna.db, the one source of a run's meta (item 20).
// meta.json is only the earlier release's copy.
func TestRunIndexReadsDynaDB(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	const id = "wf_20261020-000001-aaaaaaaa"
	writeRun(t, runstore.Meta{ID: id, Name: "w", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}, nil, "")
	if list, err := e.svc.Runs("alpha"); err != nil || list.Running != 1 {
		t.Fatalf("runs = %+v, %v", list, err)
	}
	if err := runstore.ForceStatus(id, "canceled", "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(runstore.RunsDir(), id, "meta.json")); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.Runs("alpha")
	if err != nil || len(list.Runs) != 1 || list.Runs[0].Status != "canceled" {
		t.Fatalf("runs without meta.json = %+v, %v; want the canceled run from dyna.db", list, err)
	}
}

// A stale metadata file cannot change the canonical index.
func TestRunIndexIgnoresStaleMetaWrite(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	m := runstore.Meta{ID: "wf_20261020-000002-bbbbbbbb", Name: "w", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}
	writeRun(t, m, nil, "")
	if list, err := e.svc.Runs("alpha"); err != nil || list.Running != 1 {
		t.Fatalf("runs = %+v, %v", list, err)
	}
	m.Status, m.EndedAt = "ok", time.Now()
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(runstore.RunsDir(), m.ID, "meta.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.Runs("alpha")
	if err != nil || list.Running != 1 || len(list.Runs) != 1 || list.Runs[0].Status != "running" {
		t.Fatalf("runs after the old binary's finish = %+v, %v; want running", list, err)
	}
}

// A removed run leaves the index: no later revision names it.
func TestRunIndexDropsRemovedRun(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	const id = "wf_20261020-000003-cccccccc"
	writeRun(t, runstore.Meta{ID: id, Name: "w", Status: "ok", RoomID: alpha.ID, StartedAt: time.Now(), EndedAt: time.Now()}, nil, "")
	if list, err := e.svc.Runs("alpha"); err != nil || len(list.Runs) != 1 {
		t.Fatalf("runs = %+v, %v", list, err)
	}
	if err := runstore.Remove(id); err != nil {
		t.Fatal(err)
	}
	if list, err := e.svc.Runs("alpha"); err != nil || len(list.Runs) != 0 {
		t.Fatalf("runs after Remove = %+v, %v; want none", list, err)
	}
}
