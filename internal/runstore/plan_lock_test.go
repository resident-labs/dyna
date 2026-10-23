package runstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store/storetest"
)

func TestRemoveClearsAncestryLock(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Setenv(AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_remove_plan_lock")
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	run, err := Create("finished", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "1", nil)
	if err := plan.WriteAtomic(run.Dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (ancestry.Store{DataDir: dataDir}).Stamp(context.Background(), run.Dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	path := ancestry.LockPath(dataDir, run.Meta.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed run kept ancestry lock: %v", err)
	}
}

func TestRemoveClearsAncestryRowsAndStamps(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Setenv(AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_remove_ancestry")
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	run, err := Create("finished", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "1", nil)
	s := ancestry.Store{DataDir: dataDir}
	for _, dir := range []string{run.Dir, filepath.Join(run.Dir, "workflows", "nested"), filepath.Join(run.Dir, "workflows", "empty"), filepath.Join(dataDir, "runs", run.Meta.ID+"_kept")} {
		if err := plan.WriteAtomic(dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(dir) == "empty" {
			if _, err := s.Parent(t.Context(), dir); err != nil {
				t.Fatal(err)
			}
		} else if _, err := s.Stamp(t.Context(), dir, "wf_parent"); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{run.Meta.ID, run.Meta.ID + "/workflows/nested", run.Meta.ID + "/workflows/empty", run.Meta.ID + "_kept"} {
		if _, err := db.Exec("INSERT INTO state_mirrors(key,version) VALUES(?,?)", "plans/"+key, "legacy"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	var rows, stamps int
	if err := db.QueryRow("SELECT count(*) FROM plan_ancestry").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM state_mirrors WHERE key LIKE 'plans/%'").Scan(&stamps); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || stamps != 1 {
		t.Fatalf("removal left %d ancestry rows and %d stamps; want unrelated run only", rows, stamps)
	}
	var parent string
	if err := db.QueryRow("SELECT resumed_from FROM plan_ancestry WHERE plan=?", run.Meta.ID+"_kept").Scan(&parent); err != nil || parent != "wf_parent" {
		t.Fatalf("unrelated ancestry = %q,%v", parent, err)
	}
}

func TestRemoveAncestryFailureKeepsRunAndStamps(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Setenv(AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_remove_atomic")
	run, err := Create("finished", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "1", nil)
	if err := plan.WriteAtomic(run.Dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (ancestry.Store{DataDir: dataDir}).Stamp(t.Context(), run.Dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	if _, err := db.Exec("INSERT INTO state_mirrors(key,version) VALUES(?,?)", "plans/"+run.Meta.ID, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_ancestry_delete BEFORE DELETE ON plan_ancestry BEGIN SELECT RAISE(ABORT,'keep ancestry'); END"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(run.Meta.ID); err == nil {
		t.Fatal("removal ignored ancestry failure")
	}
	for _, query := range []string{"SELECT count(*) FROM runs", "SELECT count(*) FROM plan_ancestry", "SELECT count(*) FROM state_mirrors WHERE key LIKE 'plans/%'"} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s = %d,%v; want 1", query, n, err)
		}
	}
	for _, path := range []string{run.Dir, metaLockPath(run.Meta.ID), ancestry.LockPath(dataDir, run.Meta.ID)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed removal changed %s: %v", path, err)
		}
	}
}
