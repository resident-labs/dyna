package store

import (
	"database/sql"
	_ "embed"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUpgradeRunsOneStep(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	stepsForTest := map[int][]string{schemaVersion - 1: {"CREATE TABLE probe (id INTEGER PRIMARY KEY)"}}
	if err := upgrade(db, 1, "", stepsForTest, nil); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = %s, want %d", value, schemaVersion)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
}

// floorSchema is the schema a store holds at the floor, as sqlite3 .schema printed it on the live store, without FTS's
// own tables, which the virtual tables make. It moves with the floor.
//
//go:embed testdata/floor.sql
var floorSchema string

// TestUpgradeFromTheFloor opens a store at the floor: every step from there runs, and the result is schema.sql.
func TestUpgradeFromTheFloor(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(floorSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('format','dyna-v2'),('schema_version',?),('store_id','floor'),('created_commit','')", strconv.Itoa(floorVersion)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var value string
	if err := s.Read().QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&value); err != nil || value != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = %s, want %d: %v", value, schemaVersion, err)
	}
}

// TestStepsStartAtTheFloor holds the steps to the versions a store can still be at.
func TestStepsStartAtTheFloor(t *testing.T) {
	if floorVersion > schemaVersion {
		t.Fatalf("floor %d is above schema %d", floorVersion, schemaVersion)
	}
	if want := schemaVersion - floorVersion; len(steps) != want {
		t.Errorf("%d steps, want one for each of the %d versions from the floor", len(steps), want)
	}
	for version := floorVersion; version < schemaVersion; version++ {
		if _, ok := steps[version]; !ok {
			t.Errorf("no step from version %d", version)
		}
	}
}
