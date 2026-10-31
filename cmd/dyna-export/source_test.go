package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBackupReadOnlyAndRejectSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT);
INSERT INTO meta VALUES ('room.records','1'),('room.records.import','1'),('goals.schema','5'),('goal_items.schema','2'),('questions.schema','4'),('room.issues.schema','3'),('sessions.records','7'),('sessions.import','1'),('machines.version','18'),('account.registry','11'),('web_devices.version','1'),('device_grants.version','2'),('hooks.documents','5'),('pr.state','2'),('pr.record_parts','1');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := openBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec("INSERT INTO meta VALUES ('wrong','1')"); err == nil {
		t.Fatal("backup allowed a write")
	}
	source.Close()
	if err := os.WriteFile(path+"-wal", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if source, err := openBackup(path); err == nil {
		source.Close()
		t.Fatal("backup with WAL was accepted")
	}
}
