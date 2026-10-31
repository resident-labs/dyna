package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestGlobalOperationUpgradePreservesReferences(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "fixture.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO meta VALUES ('schema_version','43');
		CREATE TABLE rooms(id TEXT PRIMARY KEY);
		INSERT INTO rooms VALUES ('room');
		CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB);
		INSERT INTO operations VALUES ('op','hostexec.run','room','owner','','owner','done','{}','{}',x'01',1,2,NULL,NULL);
		CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY REFERENCES operations(id));
		INSERT INTO hostexec_runs VALUES ('op');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", map[int][]string{43: steps[43]}, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hostexec_runs JOIN operations ON operations.id=hostexec_runs.op").Scan(&count); err != nil || count != 1 {
		t.Fatal("upgrade lost operation reference")
	}
}
