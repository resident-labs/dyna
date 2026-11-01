package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUpgradeConnectSessionsFrom48(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var old []string
	for _, line := range strings.Split(schema, "\n") {
		if strings.HasPrefix(line, "CREATE TABLE search_keys ") || strings.HasPrefix(line, "CREATE INDEX turns_session_started_at ") || strings.HasPrefix(line, "CREATE TABLE client_replays ") || strings.HasPrefix(line, "CREATE TABLE connect_devices ") || strings.HasPrefix(line, "CREATE TABLE connect_sessions ") || strings.HasPrefix(line, "CREATE TABLE connect_revocations ") || strings.HasPrefix(line, "CREATE TABLE transcript_turns ") || strings.HasPrefix(line, "CREATE UNIQUE INDEX transcript_turns_path_start ") || strings.HasPrefix(line, "CREATE INDEX pr_owners_session ") || strings.HasPrefix(line, "CREATE INDEX operations_pending_caller ") {
			continue
		}
		line = strings.Replace(line, ", generation INTEGER NOT NULL DEFAULT 0", "", 1)
		old = append(old, strings.Replace(line, ", sections BLOB", "", 1))
	}
	if _, err = db.Exec(strings.Join(old, "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO meta(key,value) VALUES ('schema_version','48')"); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 48, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeSessionWorkFrom42(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
		"CREATE TABLE rules (room TEXT, number INTEGER)",
		"CREATE TABLE deliveries (seq INTEGER)",
		"CREATE TABLE event_files (seq INTEGER)",
		"INSERT INTO meta VALUES ('schema_version','42')",
		"CREATE TABLE events (seq INTEGER PRIMARY KEY, room TEXT, from_kind TEXT, from_id TEXT, type TEXT, body BLOB)",
		"CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_seq INTEGER, started_at INTEGER, ended_at INTEGER)",
		"CREATE TABLE pr_owners (session TEXT)",
		"CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT, room TEXT, by_kind TEXT, by_id TEXT, class TEXT, state TEXT, card BLOB, args BLOB, args_sha256 BLOB, created_at INTEGER, expires_at INTEGER, decision BLOB, result BLOB)",
		"CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY, grant_id TEXT, host TEXT, scope TEXT, started_at INTEGER, timeout_ms INTEGER, ended_at INTEGER, output_bytes INTEGER, stopped TEXT, lost_at INTEGER)",
		"INSERT INTO turns(id,session,started_seq) VALUES ('turn1','session1',NULL)",
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

func TestUpgradeDropsRules(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO meta VALUES ('schema_version','45');
		CREATE TABLE rules (room TEXT, number INTEGER, PRIMARY KEY(room,number)); INSERT INTO rules VALUES ('room',1);
		CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_at INTEGER, ended_at INTEGER); CREATE TABLE pr_owners (session TEXT);
		CREATE TABLE operations (id TEXT, by_kind TEXT, by_id TEXT, created_at INTEGER, state TEXT);
		CREATE TABLE events (seq INTEGER PRIMARY KEY, id TEXT, at INTEGER, room TEXT, type TEXT, from_kind TEXT, from_id TEXT, body BLOB);
		CREATE TABLE deliveries (seq INTEGER REFERENCES events(seq));
		CREATE TABLE event_files (seq INTEGER REFERENCES events(seq));
		INSERT INTO events VALUES (1,'a',0,'room','rule.added','owner','', '{}'), (2,'b',0,'room','rule.edited','owner','', '{}'), (3,'c',0,'room','rule.removed','owner','', '{}'), (4,'d',0,'room','room.paused','owner','', '{}');
		INSERT INTO deliveries VALUES (1),(2),(3),(4); INSERT INTO event_files VALUES (1),(4)`); err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 45, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='rules'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rules table count = %d: %v", count, err)
	}
	events, err := (&Store{read: db}).Events(context.Background(), "room", 0, 10, nil)
	if err != nil || len(events) != 1 || events[0].ID != "d" {
		t.Fatalf("remaining events = %v: %v", events, err)
	}
	for _, table := range []string{"deliveries", "event_files"} {
		if err := db.QueryRow("SELECT count(*) FROM " + table + " WHERE seq=4").Scan(&count); err != nil || count != 1 {
			t.Fatalf("remaining %s = %d: %v", table, count, err)
		}
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("total %s = %d: %v", table, count, err)
		}
	}
}
