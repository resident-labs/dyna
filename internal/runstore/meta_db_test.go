package runstore

import (
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

// A run's meta lives in dyna.db (rule r10). meta.json is only the previous
// release's copy, so the run reads the same without it.
func TestRunMetaLivesInDynaDB(t *testing.T) {
	run := newTestRun(t, "wf_meta-db")
	run.Finish("ok", "null", nil)
	if err := os.Remove(filepath.Join(run.Dir, "meta.json")); err != nil {
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

// A runner from the previous release writes only meta.json. Its write is
// imported, even after this release's first import, so a run it finishes
// shows as finished (item 31).
func TestOldBinaryMetaWriteIsImported(t *testing.T) {
	run := newTestRun(t, "wf_meta-old")
	old := run.Meta
	old.Status, old.EndedAt = "ok", time.Now()
	b, _ := json.MarshalIndent(old, "", "  ")
	if err := os.WriteFile(filepath.Join(run.Dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMeta(run.Meta.ID)
	if err != nil || m.Status != "ok" {
		t.Fatalf("ReadMeta after an old-binary write = %+v, %v; want ok", m, err)
	}
	if list, err := List(); err != nil || len(list) != 1 || list[0].Status != "ok" {
		t.Fatalf("List after an old-binary write = %+v, %v; want ok", list, err)
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

// A run that an earlier release wrote is imported when it is first read.
func TestEarlierReleaseMetaIsImportedOnRead(t *testing.T) {
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

// With an old binary's meta.json in place, our write imports it first, so
// the row keeps our change instead of the next read importing the older
// file over it (the rollback helper's rule).
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
	if err != nil || m.Status != "canceled" || m.Name != "renamed by the old binary" {
		t.Fatalf("after an old file and our cancel: %+v, %v; want the old file's name and our canceled", m, err)
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

// A List whose runs all hold this binary's own meta.json copy takes no run
// lock: the lock is only for importing an earlier release's file.
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
