package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUpgradeSessionWorkFrom42(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
		"INSERT INTO meta VALUES ('schema_version','42')",
		"CREATE TABLE events (seq INTEGER PRIMARY KEY, room TEXT, from_kind TEXT, from_id TEXT, type TEXT, body BLOB)",
		"CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_seq INTEGER)",
		"CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT, room TEXT, by_kind TEXT, by_id TEXT, class TEXT, state TEXT, card BLOB, args BLOB, args_sha256 BLOB, created_at INTEGER, expires_at INTEGER, decision BLOB, result BLOB)",
		"CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY, grant_id TEXT, host TEXT, scope TEXT, started_at INTEGER, timeout_ms INTEGER, ended_at INTEGER, output_bytes INTEGER, stopped TEXT, lost_at INTEGER)",
		"INSERT INTO turns VALUES ('turn1','session1',NULL)",
		`INSERT INTO events VALUES (5,'room','engine','','turn.started','{"turn":"turn1"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	var indexes int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN ('turns_session_started','events_sender_type')").Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("indexes = %d, err = %v", indexes, err)
	}
	var seq int
	if err := db.QueryRow("SELECT started_seq FROM turns WHERE id='turn1'").Scan(&seq); err != nil || seq != 5 {
		t.Fatalf("started_seq = %d, err = %v", seq, err)
	}
}

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
