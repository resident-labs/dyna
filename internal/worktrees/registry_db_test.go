package worktrees

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestTrackKeepsRecordInDatabase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	if err := Track(Record{Path: wt, RunID: "wf_db"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(registryDir(), filepath.Base(wt)+".json")); !os.IsNotExist(err) {
		t.Fatalf("Track wrote a JSON record: %v", err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := db.QueryRow(`SELECT run_id FROM worktree_records WHERE path=?`, wt).Scan(&runID); err != nil || runID != "wf_db" {
		t.Fatalf("worktree_records run_id = %q, %v; want wf_db", runID, err)
	}
}

// TODO: legacy/shim remove later — writes a record the way the JSON build did, so the import test covers real legacy files; remove with the import.
func writeLegacyRecord(t *testing.T, rec Record) string {
	t.Helper()
	abs, err := filepath.Abs(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	abs = filepath.Clean(abs)
	name := fmt.Sprintf("%x.json", sha256.Sum256([]byte(abs)))
	if strings.HasPrefix(filepath.Base(abs), "dyna-wt-") && filepath.Dir(abs) == filepath.Clean(os.TempDir()) {
		name = filepath.Base(abs) + ".json"
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(registryDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(registryDir(), name)
	if err := fsutil.WriteFile(path, append(b, '\n'), 0o644, true); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacyJSONRecordsImportOnce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	valid := writeLegacyRecord(t, Record{Path: wt, RunID: "wf_legacy", Repo: "/repo", Head: "head", Port: 20010, RetainedAt: time.Unix(1, 0).UTC()})
	damaged := filepath.Join(registryDir(), "dyna-wt-corrupt.json")
	if err := os.WriteFile(damaged, []byte(`{not-json`), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := List()
	if err != nil || len(records) != 1 || records[0].Path != wt || records[0].RunID != "wf_legacy" || records[0].Port != 20010 {
		t.Fatalf("List() = %#v, %v; want the imported legacy record", records, err)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("imported JSON record was removed, so a rollback cannot read it: %v", err)
	}
	if _, err := os.Stat(damaged); err != nil {
		t.Fatalf("damaged JSON record was removed: %v", err)
	}
}

func TestLegacyImportRetriesAfterReadFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	rec := Record{Path: wt, RunID: "wf_retry", Repo: "/repo", Head: "head", Port: 20020, RetainedAt: time.Unix(2, 0).UTC()}
	path := writeLegacyRecord(t, rec)
	// A symlink loop fails every read, even for root, so the import sees a read error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("List() imported past an unreadable legacy record")
	}

	// Restoring the file lets a later import read it and keep its metadata and port.
	writeLegacyRecord(t, rec)
	records, err := List()
	if err != nil || len(records) != 1 || records[0].RunID != "wf_retry" || records[0].Repo != "/repo" || records[0].Port != 20020 {
		t.Fatalf("List() after restore = %#v, %v; want the retried legacy record", records, err)
	}
}

// TestLegacyRealRecordsImport imports three records copied from a real registry folder.
func TestLegacyRealRecordsImport(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(registryDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("testdata/legacy/*.json")
	if err != nil || len(files) != 3 {
		t.Fatalf("fixture files = %v, %v; want 3", files, err)
	}
	want := map[string]Record{}
	for _, src := range files {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(registryDir(), filepath.Base(src))
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		want[rec.Path] = rec
	}

	records, err := List()
	if err != nil || len(records) != len(files) {
		t.Fatalf("List() = %d records, %v; want %d", len(records), err, len(files))
	}
	for _, got := range records {
		if w, ok := want[got.Path]; !ok || !reflect.DeepEqual(w, got) {
			t.Fatalf("imported record = %#v; want %#v", got, w)
		}
	}
	for _, src := range files {
		if _, err := os.Stat(filepath.Join(registryDir(), filepath.Base(src))); err != nil {
			t.Fatalf("imported fixture was removed: %v", err)
		}
	}
}

func TestRegistryWritesUseWriterWithoutUnusedEvents(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := registryDB()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.TempDir(), "dyna-wt-publish")
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("unused event: %+v", event) })
	if err := Track(Record{Path: path, Port: 20000}); err != nil {
		t.Fatal(err)
	}
	records, err := List()
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, %v", records, err)
	}
	if err := MarkRemoved(path); err != nil {
		t.Fatal(err)
	}
	records, err = List()
	if err != nil || len(records) != 0 {
		t.Fatalf("records after removal = %v, %v", records, err)
	}
	if got := writer.Health().Writes; got != 2 {
		t.Fatalf("writer recorded %d writes, want 2", got)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_tree BEFORE INSERT ON worktree_records BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := Track(Record{Path: path, Port: 20000}); err == nil {
		t.Fatal("rejected record succeeded")
	}
}

func TestRegistryRequiresEngineMigration(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	database.DisownSchema()
	defer database.OwnSchema()
	if _, err := registryDB(); err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("opening unmigrated registry = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='worktree_records'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("registry open changed schema: count=%d, %v", count, err)
	}
}

func TestTrackRetriesPortClaimedDuringProbe(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := registryDB()
	if err != nil {
		t.Fatal(err)
	}
	paths := pathsWithBlock(t, 20000, 2)
	old := portFree
	claimed := false
	portFree = func(int) bool {
		if got := state.For(db).Health().InFlight; got != 0 {
			t.Errorf("socket probe ran during %d writes", got)
		}
		if !claimed {
			claimed = true
			// A concurrent writer claims the candidate after the first snapshot.
			if err := Track(Record{Path: paths[1], Port: 20000}); err != nil {
				t.Fatal(err)
			}
		}
		return true
	}
	t.Cleanup(func() { portFree = old })
	if err := Track(Record{Path: paths[0]}); err != nil {
		t.Fatal(err)
	}
	first, err := Get(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := Get(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if !claimed || first.Port == second.Port || first.Port != 20010 || second.Port != 20000 {
		t.Fatalf("ports after concurrent claim = %d, %d; claimed=%v", first.Port, second.Port, claimed)
	}
	if got := state.For(db).Health().Writes; got != 3 {
		t.Fatalf("writer recorded %d attempts, want two commits and one retry", got)
	}
}

func registryDir() string { return filepath.Join(store.DataDir(), "worktrees") }
