package runstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// A run index asks for the rows written after the revision it last saw, so
// an unchanged store costs it no meta reads.
func TestChangesSinceReturnsOnlyNewWrites(t *testing.T) {
	run := newTestRun(t, "wf_changes")
	first, err := ChangesSince(0)
	if err != nil || len(first.Changed) != 1 || len(first.Live) != 1 {
		t.Fatalf("first = %+v, %v; want the one run", first, err)
	}
	idle, err := ChangesSince(first.Rev)
	if err != nil || len(idle.Changed) != 0 || idle.Rev != first.Rev {
		t.Fatalf("unchanged = %+v, %v; want no rows", idle, err)
	}
	run.Finish("ok", "null", nil)
	next, err := ChangesSince(first.Rev)
	if err != nil || len(next.Changed) != 1 || next.Changed[run.Meta.ID].Status != "ok" || next.Rev <= first.Rev {
		t.Fatalf("after finish = %+v, %v; want the finished row at a newer revision", next, err)
	}
}

// A version 1 runs table gains revisions and keeps its rows.
func TestRunsSchemaOneMigrates(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dataDir := store.DataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	err = database.Migrate(db, "runs.schema", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, runsSchema); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO runs(id,session,started,body) VALUES('wf_v1','',?,'{"id":"wf_v1","status":"ok"}')`, time.Now().UnixNano())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runsDB(); err != nil {
		t.Fatal(err)
	}
	rows, top, err := metasWhere(db, "rev > 0")
	if err != nil || rows["wf_v1"].Status != "ok" || top != 1 {
		t.Fatalf("after migrating = %+v, rev %d, %v; want wf_v1 at revision 1", rows, top, err)
	}
}

// One run whose stored meta has a bad field is skipped; every other run
// still lists.
func TestListSkipsAnUndecodableRow(t *testing.T) {
	good := newTestRun(t, "wf_list-good")
	bad := filepath.Join(RunsDir(), "wf_list-bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "meta.json"), []byte(`{"id":"wf_list-bad","status":"ok","endedAt":"invalid"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil || len(list) != 1 || list[0].ID != good.Meta.ID {
		t.Fatalf("List = %+v, %v; want only the good run", list, err)
	}
}

// A meta.json that a reader cannot decode is skipped at import: it never
// becomes a row.
func TestImportSkipsAnUndecodableMeta(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := filepath.Join(RunsDir(), "wf_import-bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"id":"wf_import-bad","status":"ok","endedAt":"invalid"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	db, err := runsDB()
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow("SELECT count(*) FROM runs WHERE id='wf_import-bad'").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, %v; want the bad file skipped", rows, err)
	}
}

// A bad row stored before the import checked every field is skipped by
// readers, so it does not fail every list.
func TestListSkipsABadStoredRow(t *testing.T) {
	good := newTestRun(t, "wf_stored-good")
	db, err := runsDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(RunsDir(), "wf_stored-bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs(id,session,started,body,rev) VALUES('wf_stored-bad','',0,'{"id":"wf_stored-bad","status":"ok","endedAt":"invalid"}',99)`); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil || len(list) != 1 || list[0].ID != good.Meta.ID {
		t.Fatalf("List = %+v, %v; want only the good run", list, err)
	}
}

// The run index calls ChangesSince on every web call, so the walk over the
// run folders is reused until the runs folder changes. A new folder, even
// one with only an earlier release's meta.json, is seen at once.
func TestChangesSinceWalksOnlyWhenTheFoldersChange(t *testing.T) {
	newTestRun(t, "wf_walk-a")
	settle(t)
	walks := 0
	old := walked
	walked = func() { walks++ }
	t.Cleanup(func() { walked = old })
	for range 3 {
		if c, err := ChangesSince(0); err != nil || len(c.Live) != 1 {
			t.Fatalf("ChangesSince = %+v, %v", c, err)
		}
	}
	if walks != 1 {
		t.Fatalf("three calls with no folder change walked %d times; want once", walks)
	}
	// An earlier release starts a run: a folder with only its meta.json.
	dir := filepath.Join(RunsDir(), "wf_walk-old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"id":"wf_walk-old","status":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := ChangesSince(0); err != nil || len(c.Live) != 2 || len(c.Changed) != 1 || c.Changed["wf_walk-a"].Status != "running" {
		t.Fatalf("after a new folder = %+v, %v; want both folders and only the canonical row", c, err)
	}
	if walks != 2 {
		t.Fatalf("walks = %d; want one more for the new folder", walks)
	}
}

// settle sets the runs folder's time well in the past, so a walk is not
// racy and the cache is trusted.
func settle(t *testing.T) time.Time {
	t.Helper()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(RunsDir(), old, old); err != nil {
		t.Fatal(err)
	}
	return old
}

// A run folder made in the same clock tick as a walk leaves the runs
// folder's time unchanged. A walk that ends that close to the time is not
// trusted, so the next call walks again and sees the folder.
func TestChangesSinceSeesAFolderMadeInTheWalksTick(t *testing.T) {
	newTestRun(t, "wf_racy-a")
	if c, err := ChangesSince(0); err != nil || len(c.Live) != 1 {
		t.Fatalf("first = %+v, %v", c, err)
	}
	info, err := os.Stat(RunsDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_RUN_ID", "wf_racy-b")
	if _, err := Create("racy", "return null", nil); err != nil {
		t.Fatal(err)
	}
	// The same tick: the runs folder keeps the time the walk saw.
	if err := os.Chtimes(RunsDir(), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if c, err := ChangesSince(0); err != nil || len(c.Live) != 2 {
		t.Fatalf("after a run made in the walk's tick = %+v, %v; want both runs", c, err)
	}
}
