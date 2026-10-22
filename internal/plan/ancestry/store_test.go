package ancestry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestAncestryImportsLegacyHeaderAtStartup(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	dir := filepath.Join(dataDir, "runs", "wf_old")
	p := plan.Extract("return 1", plan.Options{ResumedFrom: "wf_parent"})
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeLegacyGraph(dir, p); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	var beforeRows int
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM plan_ancestry").Scan(&beforeRows); err != nil || beforeRows != 1 {
		t.Fatalf("startup imported %d rows: %v", beforeRows, err)
	}
	got, err := plan.ReadFile(dir)
	if err != nil || got.ResumedFrom != "wf_parent" {
		t.Fatalf("import = %#v, %v", got, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("import changed the legacy graph")
	}
	p.ResumedFrom = "wf_wrong"
	if err := writeLegacyGraph(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	got, err = plan.ReadFile(dir)
	if err != nil || got.ResumedFrom != "wf_parent" {
		t.Fatalf("legacy rewrite changed the first parent: %#v, %v", got, err)
	}
}

func TestAncestryStampKeepsFirstParentAndGraph(t *testing.T) {
	s := testAncestryStore(t)
	dir := t.TempDir()
	p := plan.Extract("return 1", plan.Options{})
	if err := plan.WriteAtomic(dir, p, nil); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var events []hub.Event
	stateWriter := func(c hub.Event) { events = append(events, c) }
	// Ancestry has no event consumer, so both commits must publish nothing.
	state.For(db).SetPublish(stateWriter)
	got, err := s.Stamp(context.Background(), dir, "wf_parent")
	if err != nil || got != "wf_parent" {
		t.Fatalf("stamp = %q,%v", got, err)
	}
	got, err = s.Stamp(context.Background(), dir, "wf_other")
	if err != nil || got != "wf_parent" {
		t.Fatalf("later stamp = %q,%v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var legacy plan.Plan
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ResumedFrom != "" || legacy.PlanHash != p.PlanHash || !legacy.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("rollback header = %#v", legacy)
	}
	if len(events) != 0 {
		t.Fatalf("published %d unused events", len(events))
	}
}

func TestAncestryIdentitySurvivesArchiveWithoutPointer(t *testing.T) {
	s := testAncestryStore(t)
	dir := filepath.Join(s.DataDir, "runs", "wf_archived", "workflows", "nested-1")
	p := plan.Extract("return 1", plan.Options{})
	if err := plan.WriteAtomic(dir, p, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stamp(context.Background(), dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(s.DataDir, "archive", "runs", "wf_archived")
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(s.DataDir, "runs", "wf_archived"), archive); err != nil {
		t.Fatal(err)
	}
	got, err := plan.ReadFile(filepath.Join(archive, "workflows", "nested-1"))
	if err != nil || got.ResumedFrom != "wf_parent" {
		t.Fatalf("archived ancestry = %#v,%v", got, err)
	}
}

func TestCancelledAncestryWriteChangesNeitherRowNorHeader(t *testing.T) {
	s := testAncestryStore(t)
	dir := t.TempDir()
	if err := plan.WriteAtomic(dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Stamp(ctx, dir, "wf_parent"); err == nil {
		t.Fatal("cancelled stamp succeeded")
	}
	got, err := s.Parent(context.Background(), dir)
	if err != nil || got != "" {
		t.Fatalf("row = %q,%v", got, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("cancelled write changed graph")
	}
}

func testAncestryStore(t *testing.T) ancestry.Store {
	t.Helper()
	dataDir := storetest.DataDir(t)
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	return ancestry.Store{DataDir: dataDir}
}

func TestAncestryReadOnlyOldStoreFailsWithoutMigrating(t *testing.T) {
	dataDir := storetest.DataDir(t)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		}
	})
	if _, err := (ancestry.Store{DataDir: dataDir}).Parent(context.Background(), t.TempDir()); err == nil {
		t.Fatal("read-only old store succeeded")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='plan_ancestry'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("new tables = %d,%v", count, err)
	}
}

func TestAncestryStampIgnoresOldBinaryParent(t *testing.T) {
	s := testAncestryStore(t)
	dir := t.TempDir()
	p := plan.Extract("return 1", plan.Options{})
	if err := plan.WriteAtomic(dir, p, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stamp(context.Background(), dir, "wf_first"); err != nil {
		t.Fatal(err)
	}
	// The previous binary rewrites the header after our stamped copy.
	p.ResumedFrom = "wf_stale"
	if err := writeLegacyGraph(dir, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stamp(context.Background(), dir, "wf_first"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Parent(context.Background(), dir)
	if err != nil || got != "wf_first" {
		t.Fatalf("row = %q,%v", got, err)
	}
	header, err := readLegacyGraph(dir)
	if err != nil || header.ResumedFrom != "wf_stale" {
		t.Fatalf("header = %#v,%v; stamping must preserve the retained header", header, err)
	}
}

func TestAncestryStartupKeepsEmptyRow(t *testing.T) {
	s := ancestry.Store{DataDir: storetest.DataDir(t)}
	t.Cleanup(func() { database.Close(s.DataDir) })
	dir := filepath.Join(s.DataDir, "runs", "wf_empty")
	if err := plan.WriteAtomic(dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if parent, err := s.Parent(t.Context(), dir); err != nil || parent != "" {
		t.Fatalf("parent = %q,%v", parent, err)
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var rows, stamps int
	if err := db.QueryRow("SELECT count(*) FROM plan_ancestry").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM state_mirrors WHERE key='plans/wf_empty'").Scan(&stamps); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || stamps != 0 {
		t.Fatalf("empty header kept %d rows and %d stamps", rows, stamps)
	}
}

func TestAncestryStartupRejectsUnreadableHeader(t *testing.T) {
	dataDir := storetest.DataDir(t)
	dir := filepath.Join(dataDir, "runs", "wf_malformed")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	if err := ancestry.Migrate(dataDir); err == nil {
		t.Fatal("startup ignored malformed legacy header")
	}
}

func TestAncestryReadKeepsReadOnlyArtifactTimes(t *testing.T) {
	s := ancestry.Store{DataDir: storetest.DataDir(t)}
	t.Cleanup(func() { database.Close(s.DataDir) })
	archive := t.TempDir()
	t.Setenv(store.RunArchiveDirEnv, archive)
	dir := filepath.Join(archive, "wf_readonly", "workflows", "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeLegacyGraph(dir, plan.Extract("return 1", plan.Options{ResumedFrom: "wf_parent"})); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var imported string
	if err := db.QueryRow("SELECT resumed_from FROM plan_ancestry WHERE plan='wf_readonly/workflows/nested'").Scan(&imported); err != nil || imported != "wf_parent" {
		t.Fatalf("ordered engine migration missed custom archive: %q, %v", imported, err)
	}
	parent, err := s.Parent(context.Background(), dir)
	if err != nil || parent != "wf_parent" {
		t.Fatalf("read-only ancestry = %q,%v", parent, err)
	}
	after, err := os.Stat(dir)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("ancestry read changed the archived directory time")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("ancestry read added an artifact file")
	}
}

func TestAncestryStartupDoesNotInspectKnownGraphs(t *testing.T) {
	s := testAncestryStore(t)
	dir := filepath.Join(s.DataDir, "runs", "wf_known")
	if err := plan.WriteAtomic(dir, plan.Extract("return 1", plan.Options{}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stamp(t.Context(), dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var parent string
	err = db.QueryRow("SELECT resumed_from FROM plan_ancestry WHERE plan=?", "wf_known").Scan(&parent)
	if err != nil || parent != "wf_parent" {
		t.Fatalf("startup inspected known graph: parent=%q, err=%v", parent, err)
	}
}

func writeLegacyGraph(dir string, p *plan.Plan) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return store.WriteJSON(filepath.Join(dir, "plan.json"), p, 0600)
}

func readLegacyGraph(dir string) (*plan.Plan, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		return nil, err
	}
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func TestAncestryStampPreservesGraphBytes(t *testing.T) {
	s := testAncestryStore(t)
	dir := t.TempDir()
	raw := []byte("{\"version\":1,\"sourceSha256\":\"unchanged\",\"resumedFrom\":\"\",\"nodes\":[],\"future\":{\"large\":9007199254740993,\"text\":\"<tag>\"}}")
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stamp(t.Context(), dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Fatalf("stamping changed graph bytes: %s", after)
	}
}
