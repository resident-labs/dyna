package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// LEGACY(v1-transfer): this test, which pins the steps from schema 42. Remove after the transfer.
func TestGlobalOperationUpgradePreservesReferences(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "fixture.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE rules (room TEXT, number INTEGER);
		CREATE TABLE hosts (name TEXT);
		CREATE TABLE deliveries (seq INTEGER);
		CREATE TABLE event_files (seq INTEGER);
		INSERT INTO meta VALUES ('schema_version','42');
		CREATE TABLE events (seq INTEGER PRIMARY KEY, room TEXT, from_kind TEXT, from_id TEXT, type TEXT, body BLOB);
		CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_seq INTEGER, started_at INTEGER, ended_at INTEGER);
		CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT, goal TEXT, approved_head TEXT, branch TEXT); CREATE TABLE repos (repo TEXT PRIMARY KEY);
		CREATE TABLE rooms(id TEXT PRIMARY KEY); CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT, parent TEXT, goal TEXT, created_at INTEGER, closed_at INTEGER); CREATE TABLE uploads (sha256 TEXT); CREATE TABLE archives (session TEXT, repo TEXT, ref TEXT, commit_sha TEXT, created_at INTEGER, expires_at INTEGER, retry_at INTEGER);
		INSERT INTO rooms VALUES ('room');
		CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB);
		INSERT INTO operations VALUES ('op','hostexec.run','room','owner','','owner','done','{}','{}',x'01',1,2,NULL,NULL);
		CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT, room TEXT, to_kind TEXT, to_id TEXT, hosts BLOB, by_kind TEXT, by_id TEXT, created_at INTEGER, expires_at INTEGER, revoked_at INTEGER, expired_at INTEGER);
		INSERT INTO grants(id) VALUES ('grant');
		CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY REFERENCES operations(id), grant_id TEXT NOT NULL REFERENCES grants(id), host TEXT NOT NULL, scope TEXT NOT NULL, started_at INTEGER NOT NULL, timeout_ms INTEGER NOT NULL, ended_at INTEGER, output_bytes INTEGER NOT NULL DEFAULT 0, stopped TEXT NOT NULL DEFAULT '', lost_at INTEGER);
		INSERT INTO hostexec_runs VALUES ('op','grant','host','scope',1,100,2,0,'',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO proxy_auths VALUES ('invented', 'kimi', x'00', 0)"); err != nil {
		t.Fatal("proxy credentials table missing after upgrade")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hostexec_runs JOIN operations ON operations.id=hostexec_runs.op").Scan(&count); err != nil || count != 1 {
		t.Fatal("upgrade lost operation reference")
	}
}
