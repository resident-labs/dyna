package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
	if err := upgrade(db, 1, "", stepsForTest); err != nil {
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

func TestUpgradeGrantsExistingRoot(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 INSERT INTO meta VALUES ('schema_version','12');
 CREATE TABLE rooms (id TEXT PRIMARY KEY);
 CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT NOT NULL, parent TEXT, created_at INTEGER NOT NULL, closed_at INTEGER);
 CREATE TABLE events (seq INTEGER PRIMARY KEY, room TEXT, type TEXT NOT NULL, body BLOB NOT NULL);
 CREATE TABLE turns (id TEXT PRIMARY KEY, ended_at INTEGER);
 INSERT INTO rooms VALUES ('room');
 INSERT INTO sessions VALUES ('root','room',NULL,1000,NULL),('child','room','root',1000,NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 1, "", steps); err != nil {
		t.Fatal(err)
	}
	s := &Store{read: db}
	grants, err := s.ListActiveGrants(context.Background(), "room", contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, time.UnixMilli(1000))
	if err != nil || len(grants) != 1 || grants[0].Mode != contract.GrantModePreapproved || grants[0].Hosts[0] != contract.AllHosts || grants[0].Commands[0].Program != "*" || !grants[0].Commands[0].Prefix {
		t.Fatalf("root grants: %+v %v", grants, err)
	}
}

// A turn that is open during the upgrade ends later and reads the posts after its start.
func TestUpgradeSetsOpenTurnStartSeq(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 INSERT INTO meta VALUES ('schema_version','16');
 CREATE TABLE turns (id TEXT PRIMARY KEY, ended_at INTEGER);
 CREATE TABLE events (seq INTEGER PRIMARY KEY, type TEXT NOT NULL, body BLOB NOT NULL);
 INSERT INTO turns VALUES ('open',NULL),('ended',2000);
 INSERT INTO events VALUES (7,'turn.started','{"turn":"ended"}'),(9,'turn.started','{"turn":"open"}');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 1, "", steps); err != nil {
		t.Fatal(err)
	}
	var seq sql.NullInt64
	if err := db.QueryRow("SELECT started_seq FROM turns WHERE id='open'").Scan(&seq); err != nil || seq.Int64 != 9 {
		t.Fatalf("open turn started_seq %v: %v", seq, err)
	}
}
