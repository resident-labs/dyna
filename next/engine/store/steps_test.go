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
 CREATE TABLE operations (kind TEXT, state TEXT, args BLOB, decision BLOB);
 INSERT INTO rooms VALUES ('room');
 INSERT INTO sessions VALUES ('root','room',NULL,1000,NULL),('child','room','root',1000,NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 1, "", map[int][]string{12: steps[12], 13: steps[13], 14: steps[14], 15: steps[15], 16: steps[16], 17: steps[17], 18: {}, 19: {}, 20: {}, 21: {}, 22: {}, 23: {}, 24: {}, 25: {}, 26: {}, 27: {}, 28: {}, 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {}, 37: {}, 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	s := &Store{read: db}
	grants, err := s.ListActiveGrants(context.Background(), "room", contract.Principal{Kind: contract.PrincipalSession, Session: "root"}, time.UnixMilli(1000))
	if err != nil || len(grants) != 1 || grants[0].Hosts[0] != contract.AllHosts {
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
	if err = upgrade(db, 1, "", map[int][]string{16: steps[16], 17: {}, 18: steps[18], 19: {}, 20: steps[20], 21: {}, 22: {}, 23: {}, 24: {}, 25: {}, 26: {}, 27: {}, 28: {}, 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {}, 37: {}, 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	var seq sql.NullInt64
	if err := db.QueryRow("SELECT started_seq FROM turns WHERE id='open'").Scan(&seq); err != nil || seq.Int64 != 9 {
		t.Fatalf("open turn started_seq %v: %v", seq, err)
	}
}

func TestUpgradeRevokesOldGrants(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 INSERT INTO meta VALUES ('schema_version','17');
 CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT NOT NULL, parent TEXT);
 INSERT INTO sessions VALUES ('root','room',NULL),('child','room','root');
 CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL, to_kind TEXT NOT NULL, to_id TEXT NOT NULL, mode TEXT NOT NULL, hosts BLOB NOT NULL, commands BLOB NOT NULL, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, revoked_at INTEGER, expired_at INTEGER);
 INSERT INTO grants VALUES
 ('grant_root','hostexec','room','session','root','preapproved','["*"]','[]','engine','',NULL,NULL),
 ('custom','hostexec','room','session','root','approve','[]','[]','owner','',NULL,NULL),
 ('grant_child','hostexec','room','session','child','preapproved','[]','[]','engine','',NULL,NULL),
 ('expired','hostexec','room','session','child','approve','[]','[]','owner','',NULL,5),
 ('old','hostexec','room','session','child','approve','[]','[]','owner','',7,NULL);
 CREATE TABLE operations (kind TEXT, state TEXT, args BLOB, decision BLOB);
 CREATE TABLE hostexec_runs (grant_id TEXT, ended_at INTEGER, stopped TEXT);
 INSERT INTO hostexec_runs VALUES ('custom',NULL,''),('grant_root',NULL,'');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 1, "", map[int][]string{17: steps[17], 18: {}, 19: {}, 20: {}, 21: {}, 22: {}, 23: {}, 24: {}, 25: {}, 26: {}, 27: {}, 28: {}, 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {}, 37: {}, 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id,revoked_at,reason FROM grants ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]sql.NullInt64{}
	for rows.Next() {
		var id, reason string
		var revoked sql.NullInt64
		if err := rows.Scan(&id, &revoked, &reason); err != nil {
			t.Fatal(err)
		}
		if reason != "" {
			t.Fatalf("%s reason %q", id, reason)
		}
		got[id] = revoked
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["grant_root"].Valid || !got["custom"].Valid || !got["grant_child"].Valid || got["expired"].Valid || got["old"].Int64 != 7 {
		t.Fatalf("revocations: %+v", got)
	}
	var stopped string
	if err := db.QueryRow(`SELECT stopped FROM hostexec_runs WHERE grant_id='custom'`).Scan(&stopped); err != nil || stopped != "revoked" {
		t.Fatalf("run stopped=%q: %v", stopped, err)
	}
	if _, err := db.Exec(`SELECT mode,commands FROM grants`); err == nil {
		t.Fatal("old columns remain")
	}
}

func TestUpgradeBoxIntent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 INSERT INTO meta VALUES ('schema_version','21');
 CREATE TABLE sessions (id TEXT PRIMARY KEY, state TEXT NOT NULL);
 CREATE TABLE boxes (id TEXT PRIMARY KEY, session TEXT NOT NULL);
 INSERT INTO sessions VALUES ('a','dormant'),('b','closing'),('c','open'),('d','pending');
 INSERT INTO boxes VALUES ('a','a'),('b','b'),('c','c'),('d','d');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", map[int][]string{21: steps[21], 22: {}, 23: {}, 24: {}, 25: {}, 26: {}, 27: {}, 28: {}, 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {}, 37: {}, 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT b.want,s.state,b.due_at,b.failures FROM boxes b JOIN sessions s ON s.id=b.session ORDER BY b.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for _, want := range []struct{ intent, state string }{{"stop", "open"}, {"close", "closing"}, {"run", "open"}, {"run", "open"}} {
		if !rows.Next() {
			t.Fatal("missing box")
		}
		var intent, state string
		var due, failures int
		if err := rows.Scan(&intent, &state, &due, &failures); err != nil {
			t.Fatal(err)
		}
		if intent != want.intent || state != want.state || due != 0 || failures != 0 {
			t.Fatalf("got %s %s %d %d", intent, state, due, failures)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeRecoversPRBranchAfterHandoff(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
 INSERT INTO meta VALUES ('schema_version','28');
 CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT);
 INSERT INTO pr_owners VALUES ('owner/repo',42,'parent'),('owner/repo',43,'parent');
 CREATE TABLE events (seq INTEGER PRIMARY KEY, type TEXT, body BLOB);
 INSERT INTO events VALUES (1,'pr.opened','{"repo":"owner/repo","number":42,"session":"child"}');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", map[int][]string{28: steps[28], 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {}, 37: {}, 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	var branch, unknown string
	if err := db.QueryRow("SELECT branch FROM pr_owners WHERE number=42").Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT branch FROM pr_owners WHERE number=43").Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if branch != "v2/child" || unknown != "" {
		t.Fatalf("branches %q, %q", branch, unknown)
	}
}

func TestUpgradeIndexesExistingPosts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO meta VALUES('schema_version','37');
 CREATE TABLE sessions(id TEXT PRIMARY KEY,name TEXT);
 INSERT INTO sessions VALUES('s','worker');
 CREATE TABLE events(seq INTEGER PRIMARY KEY,type TEXT,body BLOB,from_kind TEXT,from_id TEXT);
 INSERT INTO events VALUES(1,'post.created','{"text":"auto-deploy"}','session','s');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 1, "", map[int][]string{37: steps[37], 38: {}, 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM post_search WHERE post_search MATCH 'deploy* AND work*'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backfilled posts: %d, %v", count, err)
	}
}

func TestUpgradeMarksLegacyApprovedAsRunning(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO meta VALUES ('schema_version','38'); CREATE TABLE operations (state TEXT); INSERT INTO operations VALUES ('approved'),('done');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", map[int][]string{38: steps[38], 39: {}}, nil); err != nil {
		t.Fatal(err)
	}
	var running, done int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE state='running'), count(*) FILTER (WHERE state='done') FROM operations`).Scan(&running, &done); err != nil || running != 1 || done != 1 {
		t.Fatalf("running %d done %d: %v", running, done, err)
	}
}
