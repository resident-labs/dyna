package runstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestRun(t *testing.T, id string) *Run {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", id)
	t.Setenv(SessionEnv, "")
	run, err := Create("meta", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// A run's meta lives in dyna.db and creates no metadata file.
func TestRunMetaLivesInDynaDB(t *testing.T) {
	run := newTestRun(t, "wf_meta-db")
	run.Finish("ok", "null", nil)
	if _, err := os.Stat(filepath.Join(run.Dir, "meta.json")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	m, err := ReadMeta(run.Meta.ID)
	if err != nil || m.Status != "ok" || m.EndedAt.IsZero() {
		t.Fatalf("ReadMeta without meta.json = %+v, %v; want the finished run from dyna.db", m, err)
	}
	list, err := List()
	if err != nil || len(list) != 1 || list[0].Status != "ok" {
		t.Fatalf("List = %+v, %v; want the run from dyna.db", list, err)
	}
}

// A stale rollback copy cannot overwrite canonical metadata.
func TestOldBinaryMetaWriteCannotOverwriteRow(t *testing.T) {
	run := newTestRun(t, "wf_meta-old")
	old := run.Meta
	old.Status, old.EndedAt = "ok", time.Now()
	b, _ := json.MarshalIndent(old, "", "  ")
	if err := os.WriteFile(filepath.Join(run.Dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMeta(run.Meta.ID)
	if err != nil || m.Status != "running" {
		t.Fatalf("ReadMeta after an old-binary write = %+v, %v; want running", m, err)
	}
	if list, err := List(); err != nil || len(list) != 1 || list[0].Status != "running" {
		t.Fatalf("List after an old-binary write = %+v, %v; want running", list, err)
	}
}

// A cancel and the run's own Finish can race. Only a running run changes
// status, so whichever lands first stays.
func TestCancelRacingFinishKeepsTheFirst(t *testing.T) {
	run := newTestRun(t, "wf_meta-race")
	if err := ForceStatus(run.Meta.ID, "canceled", "canceled (process no longer alive)"); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Status != "canceled" {
		t.Fatalf("after cancel then finish: %+v, %v; want canceled", m, err)
	}
	if run.Meta.Status != "canceled" {
		t.Fatalf("Finish left the run's own meta at %q; want the stored canceled", run.Meta.Status)
	}

	other := newTestRun(t, "wf_meta-race2")
	other.Finish("ok", "null", nil)
	if err := ForceStatus(other.Meta.ID, "canceled", "late"); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(other.Meta.ID); err != nil || m.Status != "ok" || m.Error != "" {
		t.Fatalf("after finish then cancel: %+v, %v; want ok", m, err)
	}
}

// Startup imports a run that an earlier release wrote.
func TestEarlierReleaseMetaIsImportedAtStartup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := filepath.Join(RunsDir(), "wf_earlier")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Meta{ID: "wf_earlier", Status: "ok", StartedAt: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := runsDB()
	if err != nil {
		t.Fatal(err)
	}
	if list, err := List(); err != nil || len(list) != 1 || list[0].Status != "ok" {
		t.Fatalf("List = %+v, %v; want the earlier release's run", list, err)
	}
	if m, ok, err := storedMeta(db, "wf_earlier"); err != nil || !ok || m.Status != "ok" {
		t.Fatalf("row = %+v, %v, %v; want imported", m, ok, err)
	}
}

// A canonical update ignores changes made only to a stale rollback copy.
func TestWriteAfterOldBinaryFileKeepsOurValue(t *testing.T) {
	run := newTestRun(t, "wf_meta-order")
	old := run.Meta
	old.Name = "renamed by the old binary"
	b, _ := json.MarshalIndent(old, "", "  ")
	if err := os.WriteFile(filepath.Join(run.Dir, "meta.json.tmp"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// An old binary writes by rename.
	if err := os.Rename(filepath.Join(run.Dir, "meta.json.tmp"), filepath.Join(run.Dir, "meta.json")); err != nil {
		t.Fatal(err)
	}
	if err := ForceStatus(run.Meta.ID, "canceled", "stopped"); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMeta(run.Meta.ID)
	if err != nil || m.Status != "canceled" || m.Name != "meta" {
		t.Fatalf("after an old file and our cancel: %+v, %v; want the canonical name and canceled status", m, err)
	}
}

// Removing a run deletes its row, then its folder.
func TestRemoveDeletesTheRow(t *testing.T) {
	run := newTestRun(t, "wf_meta-rm")
	run.Finish("ok", "null", nil)
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMeta(run.Meta.ID); !os.IsNotExist(err) {
		t.Fatalf("ReadMeta after Remove = %v; want not exist", err)
	}
}

// Removing a run removes its step records without touching other runs.
func TestRemoveDeletesStepRecords(t *testing.T) {
	run := newTestRun(t, "wf_step-rm")
	ctx := context.Background()
	for _, id := range []string{run.Meta.ID, "wf_other"} {
		if err := StepIntent(ctx, id, "call", 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := StepResult(ctx, id, "call", 1, 1, "shell", "output", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.Finish("ok", "null", nil); err != nil {
		t.Fatal(err)
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	db, err := runsDB()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{run.Meta.ID: 0, "wf_other": 1} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM workflow_step_records WHERE run_id=?", id).Scan(&count); err != nil || count != want {
			t.Fatalf("records for %s = %d, %v; want %d", id, count, err, want)
		}
	}
}

// Listing canonical rows takes no per-run write lock.
func TestListOfOurOwnRunsTakesNoLock(t *testing.T) {
	run := newTestRun(t, "wf_meta-nolock")
	run.Finish("ok", "null", nil)
	locks := 0
	old := lockTaken
	lockTaken = func() { locks++ }
	t.Cleanup(func() { lockTaken = old })
	if list, err := List(); err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if locks != 0 {
		t.Fatalf("List took %d run locks; want none", locks)
	}
}

// Removing a run also removes its lock file, so run-locks does not grow.
func TestRemoveDeletesTheLockFile(t *testing.T) {
	run := newTestRun(t, "wf_meta-rmlock")
	run.Finish("ok", "null", nil)
	if _, err := os.Stat(metaLockPath(run.Meta.ID)); err != nil {
		t.Fatalf("lock file before Remove: %v", err)
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metaLockPath(run.Meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("lock file after Remove: %v; want gone", err)
	}
}
