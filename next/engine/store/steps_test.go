package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// beforeSeenColumns puts back the two pr_owners columns that step 69 drops.
func beforeSeenColumns(text string) string {
	return strings.Replace(text, "seen_reviews INTEGER NOT NULL DEFAULT 0,", "seen_reviews INTEGER NOT NULL DEFAULT 0, seen_review_comments INTEGER NOT NULL DEFAULT 0, seen_comments INTEGER NOT NULL DEFAULT 0,", 1)
}

// beforeFeeds is the schema as step 70 finds it.
func beforeFeeds(text string) string {
	text = withoutPools(beforeSeenColumns(text[:strings.Index(text, "CREATE TABLE github_feeds")]))
	return strings.Replace(text, "state TEXT NOT NULL, own_wake_at", "state TEXT NOT NULL, seen_comments INTEGER NOT NULL DEFAULT 0, own_wake_at", 1)
}

// beforeWorkflows is the schema without the tables of steps 66 and 67, which come last in the file, and with the pr_owners columns of the steps that followed undone.
var beforeWorkflows = withoutPools(beforeSeenColumns(strings.Replace(schema[:strings.Index(schema, "CREATE TABLE workflow_runs")], ", meta TEXT NOT NULL DEFAULT '', meta_wake_at INTEGER NOT NULL DEFAULT 0", "", 1)))

func TestUpgradeRenamesGitHubEventsAndKeepsDefaultsQuiet(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := withoutPools(schema[:strings.Index(schema, "CREATE TABLE subscription_where")])
	if _, err = db.Exec(before); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','71');
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
		INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
		INSERT INTO events(id,at,room,type,from_kind,from_id,body) VALUES('e1',1,'r','pr.changed','engine','','{}'),('e2',2,'r','repo.main_updated','engine','','{}'),('e3',3,'r','ci.finished','engine','','{}'),('e4',4,'r','post.created','engine','','{}');
		INSERT INTO subscriptions(id,scope,room,every_ms,wake_kind,wake_id,run_image,run_size,created_at) VALUES('sub_r','room','r',0,'session','s','','',0),('sub_mine','room','r',0,'session','s','','',0);
		INSERT INTO subscription_events(sub,event_type) VALUES('sub_r','notice.posted'),('sub_r','pr.opened'),('sub_r','pr.merged'),('sub_r','pr.closed'),('sub_r','ci.finished'),('sub_r','repo.main_updated'),('sub_mine','pr.merged'),('sub_mine','issue.changed')`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	list := func(query string) string {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			out = append(out, value)
		}
		return strings.Join(out, " ")
	}
	if got, want := list("SELECT type FROM events ORDER BY id"), "github.pr.changed git.main_updated github.ci.finished post.created"; got != want {
		t.Fatalf("events: %s", got)
	}
	if got, want := list("SELECT event_type FROM subscription_events WHERE sub='sub_r' ORDER BY event_type"), "git.main_updated notice.posted"; got != want {
		t.Fatalf("default: %s", got)
	}
	if got, want := list("SELECT event_type FROM subscription_events WHERE sub='sub_mine' ORDER BY event_type"), "github.issue.changed github.pr.merged"; got != want {
		t.Fatalf("a subscription of the owner's: %s", got)
	}
}

func TestUpgradeGivesTrackedPRsNoMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := strings.Replace(schema[:strings.Index(schema, "CREATE TABLE issue_owners")], ", meta TEXT NOT NULL DEFAULT '', meta_wake_at INTEGER NOT NULL DEFAULT 0", "", 1)
	if _, err = db.Exec(withLastHost(beforeSeenColumns(before))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','67');
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
		INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
		INSERT INTO pr_owners(repo,number,session,goal,approved_head,baseline) VALUES('o/r',7,'s','','',0)`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var meta string
	var baseline bool
	if err = db.QueryRow("SELECT meta,baseline FROM pr_owners WHERE number=7").Scan(&meta, &baseline); err != nil || meta != "" || baseline {
		t.Fatalf("meta %q baseline %v: %v", meta, baseline, err)
	}
}

// LEGACY(v1-transfer): every test here but TestUpgradeRunsOneStep, which pin steps 42 to 63. Remove after the transfer.
func TestUpgradeConnectSessionsFrom48(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var old []string
	for _, line := range strings.Split(beforeWorkflows, "\n") {
		line = strings.Replace(line, ", main_head TEXT NOT NULL DEFAULT ''", "", 1)
		if strings.HasPrefix(line, "CREATE TABLE subscription") || strings.HasPrefix(line, "CREATE INDEX subscriptions_") || strings.HasPrefix(line, "CREATE TABLE repo_ci_hosts ") || strings.HasPrefix(line, "CREATE TABLE search_keys ") || strings.HasPrefix(line, "CREATE INDEX turns_session_started_at ") || strings.HasPrefix(line, "CREATE TABLE client_replays ") || strings.HasPrefix(line, "CREATE TABLE connect_devices ") || strings.HasPrefix(line, "CREATE TABLE connect_sessions ") || strings.HasPrefix(line, "CREATE TABLE connect_revocations ") || strings.HasPrefix(line, "CREATE TABLE transcript_turns ") || strings.HasPrefix(line, "CREATE UNIQUE INDEX transcript_turns_path_start ") || strings.HasPrefix(line, "CREATE INDEX pr_owners_session ") || strings.HasPrefix(line, "CREATE INDEX operations_pending_caller ") || strings.HasPrefix(line, "CREATE TABLE disk_warnings ") || strings.HasPrefix(line, "CREATE TABLE room_leftovers ") || strings.HasPrefix(line, "CREATE TABLE session_accounts ") || strings.HasPrefix(line, "CREATE TABLE questions ") || strings.HasPrefix(line, "CREATE INDEX questions_") || strings.HasPrefix(line, "CREATE TABLE goals ") || strings.HasPrefix(line, "CREATE INDEX goals_holder ") || strings.HasPrefix(line, "CREATE TABLE goal_items ") || strings.HasPrefix(line, "CREATE TABLE goal_journal ") || strings.HasPrefix(line, "CREATE TABLE checkins ") {
			continue
		}
		line = strings.Replace(line, ", generation INTEGER NOT NULL DEFAULT 0", "", 1)
		line = strings.Replace(line, ", disk_floor INTEGER NOT NULL DEFAULT 21474836480, disk_low_since INTEGER NOT NULL DEFAULT 0", "", 1)
		line = strings.Replace(line, ", state TEXT NOT NULL DEFAULT 'open', reason TEXT NOT NULL DEFAULT ''", "", 1)
		line = strings.Replace(line, ", dispatched_model TEXT NOT NULL DEFAULT ''", "", 1)
		line = strings.Replace(line, ", compose TEXT NOT NULL DEFAULT ''", "", 1)
		old = append(old, strings.Replace(line, ", sections BLOB", "", 1))
	}
	old = append(old, "CREATE TABLE uploads (sha256 TEXT NOT NULL, room TEXT NOT NULL, PRIMARY KEY(sha256, room));")
	if _, err = db.Exec(strings.Join(old, "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO meta(key,value) VALUES ('schema_version','48'); INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at,retry_at) VALUES('s','o/r','refs/dyna/archive/s','abc',1,2,3)"); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 48, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var ref string
	var retry int
	if err = db.QueryRow("SELECT ref,retry_at FROM archives WHERE session='s'").Scan(&ref, &retry); err != nil || ref != "refs/dyna/archive/s" || retry != 3 {
		t.Fatalf("archive kept: %q %d: %v", ref, retry, err)
	}
	var state string
	if _, err = db.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0)"); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT state FROM rooms WHERE id='r'").Scan(&state); err != nil || state != "open" {
		t.Fatalf("room state %q: %v", state, err)
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
		"CREATE TABLE rooms (id TEXT PRIMARY KEY); CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT, parent TEXT, goal TEXT, created_at INTEGER, closed_at INTEGER); CREATE TABLE uploads (sha256 TEXT); CREATE TABLE archives (session TEXT, repo TEXT, ref TEXT, commit_sha TEXT, created_at INTEGER, expires_at INTEGER, retry_at INTEGER);",
		"CREATE TABLE hosts (name TEXT)",
		"CREATE TABLE deliveries (seq INTEGER)",
		"CREATE TABLE event_files (seq INTEGER)",
		"INSERT INTO meta VALUES ('schema_version','42')",
		"CREATE TABLE events (seq INTEGER PRIMARY KEY, room TEXT, from_kind TEXT, from_id TEXT, type TEXT, body BLOB)",
		"CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_seq INTEGER, started_at INTEGER, ended_at INTEGER)",
		"CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT, goal TEXT, approved_head TEXT, branch TEXT); CREATE TABLE repos (repo TEXT PRIMARY KEY)",
		"CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT, room TEXT, by_kind TEXT, by_id TEXT, class TEXT, state TEXT, card BLOB, args BLOB, args_sha256 BLOB, created_at INTEGER, expires_at INTEGER, decision BLOB, result BLOB)",
		"CREATE TABLE grants (id TEXT, kind TEXT, room TEXT, to_kind TEXT, to_id TEXT, hosts BLOB, by_kind TEXT, by_id TEXT, created_at INTEGER, expires_at INTEGER, revoked_at INTEGER, expired_at INTEGER)",
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
		CREATE TABLE rooms (id TEXT PRIMARY KEY); CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT, parent TEXT, goal TEXT, created_at INTEGER, closed_at INTEGER); CREATE TABLE uploads (sha256 TEXT); CREATE TABLE archives (session TEXT, repo TEXT, ref TEXT, commit_sha TEXT, created_at INTEGER, expires_at INTEGER, retry_at INTEGER);
		CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT, started_at INTEGER, ended_at INTEGER); CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT, goal TEXT, approved_head TEXT, branch TEXT); CREATE TABLE repos (repo TEXT PRIMARY KEY); CREATE TABLE hosts (name TEXT);
		CREATE TABLE operations (id TEXT, kind TEXT, args BLOB, by_kind TEXT, by_id TEXT, created_at INTEGER, state TEXT);
		CREATE TABLE grants (id TEXT, kind TEXT, room TEXT, to_kind TEXT, to_id TEXT, hosts BLOB, by_kind TEXT, by_id TEXT, created_at INTEGER, expires_at INTEGER, revoked_at INTEGER, expired_at INTEGER);
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

func TestPermanentHostexecGrantMigration(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); INSERT INTO meta VALUES('schema_version','53');
 CREATE TABLE grants(id TEXT,kind TEXT,room TEXT,to_kind TEXT,to_id TEXT,hosts BLOB,by_kind TEXT,by_id TEXT,created_at INTEGER,expires_at INTEGER,revoked_at INTEGER,expired_at INTEGER);
 INSERT INTO grants(id,kind,expires_at,revoked_at,expired_at) VALUES('active','hostexec',4102444800000,NULL,NULL),('past','hostexec',1,NULL,NULL),('revoked','hostexec',4102444800000,1,NULL);
 CREATE TABLE operations(kind TEXT,state TEXT,args BLOB);
 INSERT INTO operations VALUES('hostexec.grant','pending','{"expires":"future","never":false,"reason":"x"}');
 CREATE TABLE events (seq INTEGER PRIMARY KEY, type TEXT); CREATE TABLE rooms (id TEXT PRIMARY KEY); CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT, parent TEXT, goal TEXT, created_at INTEGER, closed_at INTEGER); CREATE TABLE uploads (sha256 TEXT); CREATE TABLE archives (session TEXT, repo TEXT, ref TEXT, commit_sha TEXT, created_at INTEGER, expires_at INTEGER, retry_at INTEGER);
 CREATE TABLE hosts(name TEXT); CREATE TABLE turns(id TEXT); CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT, goal TEXT, approved_head TEXT, branch TEXT); CREATE TABLE repos (repo TEXT PRIMARY KEY);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	var active, past, revoked int
	if err := db.QueryRow(`SELECT sum(id='active' AND expires_at IS NULL),sum(id='past' AND expired_at=1),sum(id='revoked' AND expires_at IS NOT NULL) FROM grants`).Scan(&active, &past, &revoked); err != nil || active != 1 || past != 1 || revoked != 1 {
		t.Fatalf("grants: %d %d %d: %v", active, past, revoked, err)
	}
	var state, args string
	if err := db.QueryRow(`SELECT state,args FROM operations`).Scan(&state, &args); err != nil || state != "expired" || args != `{"reason":"x"}` {
		t.Fatalf("operation %q %q: %v", state, args, err)
	}
}

func TestOpenRootsGetTheAskGrant(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); INSERT INTO meta VALUES('schema_version','61');
 CREATE TABLE pr_owners (repo TEXT, number INTEGER, session TEXT, goal TEXT, approved_head TEXT, branch TEXT); CREATE TABLE repos (repo TEXT PRIMARY KEY);
 CREATE TABLE events (seq INTEGER PRIMARY KEY, type TEXT); CREATE TABLE rooms (id TEXT PRIMARY KEY, state TEXT NOT NULL DEFAULT 'open'); CREATE TABLE hosts (name TEXT PRIMARY KEY);
 CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT, parent TEXT, created_at INTEGER, closed_at INTEGER, goal TEXT);
 CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL, to_kind TEXT NOT NULL, to_id TEXT NOT NULL, hosts BLOB NOT NULL, expires_at INTEGER, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER, expired_at INTEGER, reason TEXT NOT NULL DEFAULT '');
 INSERT INTO rooms(id) VALUES('room');
 INSERT INTO sessions VALUES('root','room',NULL,5,NULL,''),('child','room','root',6,NULL,''),('gone','room',NULL,7,9,'');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgrade(db, 1, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	var to string
	if err := db.QueryRow(`SELECT group_concat(to_id) FROM grants WHERE kind='ask-owner' AND revoked_at IS NULL AND expired_at IS NULL`).Scan(&to); err != nil || to != "root" {
		t.Fatalf("grants: %q: %v", to, err)
	}
}

// oldQuestions is what the step for meanwhile and blocked added to the questions table.
const oldQuestions = ", meanwhile TEXT NOT NULL DEFAULT '', blocked INTEGER NOT NULL DEFAULT 0"

// The move's rollback undoes this one step, so it must name it.
func TestMoveScriptUndoesTheRightStep(t *testing.T) {
	script, err := os.ReadFile("../../scripts/shared-folder-move.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(steps[69], "\n"), "RENAME COLUMN last_host TO run_host") || !strings.Contains(string(script), `= 70 ] || die "this undoes schema 70 only"`) {
		t.Fatal("the rollback in scripts/shared-folder-move.sh does not match step 69")
	}
}

// withLastHost gives the schema before the shared folder step: it puts back the column that step renames and takes out the pools step.
func withLastHost(schema string) string {
	return withoutPools(strings.Replace(schema, "run_host TEXT", "last_host TEXT", 1))
}

// withoutPools gives the schema before step 72.
func withoutPools(schema string) string {
	for _, added := range []string{", only_hosts TEXT NOT NULL DEFAULT '[]'", ", pool TEXT NOT NULL DEFAULT '', box TEXT NOT NULL DEFAULT ''"} {
		schema = strings.Replace(schema, added, "", 1)
	}
	lines := strings.Split(schema, "\n")
	return strings.Join(slices.DeleteFunc(lines, func(line string) bool { return strings.HasPrefix(line, "CREATE TABLE pools ") }), "\n")
}

func TestUpgradeAddsPoolsAndLeavesHostsAlone(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(withoutPools(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','72');
		INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','h','ready',1,1,1,0);
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var pool, only string
	if err = db.QueryRow("SELECT h.pool,r.only_hosts FROM hosts h,rooms r").Scan(&pool, &only); err != nil || pool != "" || only != "[]" {
		t.Fatalf("pool %q, rule %q, err %v", pool, only, err)
	}
}

// The sizes of the old per-host folders say nothing about the central one, so the next measure starts the table again.
func TestUpgradeMakesOneSizePerRoom(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const central = "CREATE TABLE room_shared (room TEXT PRIMARY KEY, bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL);"
	if !strings.Contains(schema, central) {
		t.Fatal("schema.sql lost the central sizes")
	}
	old := strings.Replace(withLastHost(beforeFeeds(schema)), central, "CREATE TABLE room_shared (room TEXT NOT NULL, host TEXT NOT NULL REFERENCES hosts(name), bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL, PRIMARY KEY(room, host));", 1)
	if _, err = db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','69');
		INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','h','ready',1,1,1,0);
		INSERT INTO room_shared(room,host,bytes,measured_at) VALUES('r','h',7,0);
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
		INSERT INTO subscriptions(id,scope,room,every_ms,wake_kind,wake_id,run_image,run_size,created_at,last_host) VALUES('s','room','r',1000,'','','base','small',0,'h')`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = db.QueryRow("SELECT count(*) FROM room_shared").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, err = %v", rows, err)
	}
	var pin string
	if err = db.QueryRow("SELECT run_host FROM subscriptions WHERE id='s'").Scan(&pin); err != nil || pin != "" {
		t.Fatalf("a Run's last host became its pin: %q, err = %v", pin, err)
	}
}

func TestUpgradeLeavesOldQuestionsWithoutAPlan(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.Contains(schema, oldQuestions) {
		t.Fatal("schema.sql lost the question columns")
	}
	if _, err = db.Exec(withLastHost(strings.Replace(beforeWorkflows, oldQuestions, "", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','65');
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
		INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',1);
		INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q_1','r','root','which?','pending',5)`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var text, state, meanwhile string
	var blocked bool
	if err = db.QueryRow("SELECT text,state,meanwhile,blocked FROM questions WHERE id='q_1'").Scan(&text, &state, &meanwhile, &blocked); err != nil {
		t.Fatal(err)
	}
	if text != "which?" || state != "pending" || meanwhile != "" || blocked {
		t.Fatalf("question: %q %q %q %v", text, state, meanwhile, blocked)
	}
}

func TestUpgradeMakesStartGoalsReal(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	var old []string
	for _, line := range strings.Split(beforeWorkflows, "\n") {
		line = strings.Replace(line, ", main_head TEXT NOT NULL DEFAULT ''", "", 1)
		if strings.HasPrefix(line, "CREATE TABLE subscription") || strings.HasPrefix(line, "CREATE INDEX subscriptions_") || strings.HasPrefix(line, "CREATE TABLE goals ") || strings.HasPrefix(line, "CREATE INDEX goals_holder ") || strings.HasPrefix(line, "CREATE TABLE goal_items ") || strings.HasPrefix(line, "CREATE TABLE goal_journal ") || strings.HasPrefix(line, "CREATE TABLE checkins ") {
			continue
		}
		old = append(old, strings.Replace(line, oldQuestions, "", 1))
	}
	if _, err = db.Exec(strings.Join(old, "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','62');
		INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0),('q','q',0,0,0);
		INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','run the room',1),('a','r','a','root','p','open','first',2),('b','r','b','root','p','open','second',3),('none','r','none','root','p','open','',4),('q','q','q',NULL,'p','open','',1),('c','q','c','q','p','open','third',5);
		INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES('gone','r','gone','root','p','closed','old',1,2)`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, floorVersion, floorCommit, steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT room,number,session,text,state FROM goals ORDER BY room,number")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var room, session, text, state string
		var number int
		if err := rows.Scan(&room, &number, &session, &text, &state); err != nil {
			t.Fatal(err)
		}
		got = append(got, room+" "+strconv.Itoa(number)+" "+session+" "+text+" "+state)
	}
	want := "q 1 c third active|r 1 a first active|r 2 b second active"
	if strings.Join(got, "|") != want {
		t.Fatalf("goals %v, want %s", got, want)
	}
}

func TestUpgradeTrackedPRsFrom64(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var old []string
	for _, line := range strings.Split(beforeWorkflows, "\n") {
		line = strings.Replace(strings.Replace(line, ", main_head TEXT NOT NULL DEFAULT ''", "", 1), oldQuestions, "", 1)
		if strings.HasPrefix(line, "CREATE INDEX pr_owners_state ") {
			continue
		}
		if strings.HasPrefix(line, "CREATE TABLE pr_owners ") {
			line = "CREATE TABLE pr_owners (repo TEXT NOT NULL REFERENCES repos(repo), number INTEGER NOT NULL, session TEXT NOT NULL REFERENCES sessions(id), goal TEXT NOT NULL, approved_head TEXT NOT NULL, branch TEXT NOT NULL DEFAULT '', PRIMARY KEY(repo, number));"
		}
		old = append(old, line)
	}
	if _, err = db.Exec(withLastHost(strings.Join(old, "\n"))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES ('schema_version','64');
INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO repos(repo,default_room) VALUES('o/r','r');
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO pr_owners(repo,number,session,goal,approved_head,branch) VALUES('o/r',7,'s','g','abc','v2/s')`); err != nil {
		t.Fatal(err)
	}
	if err = upgrade(db, 64, "", steps, nil); err != nil {
		t.Fatal(err)
	}
	if err = compareSchema(db); err != nil {
		t.Fatal(err)
	}
	var goal, approved, branch, state string
	var baseline bool
	if err = db.QueryRow("SELECT goal,approved_head,branch,state,baseline FROM pr_owners WHERE repo='o/r' AND number=7").Scan(&goal, &approved, &branch, &state, &baseline); err != nil {
		t.Fatal(err)
	}
	if goal != "g" || approved != "abc" || branch != "v2/s" || state != "open" || !baseline {
		t.Fatalf("%q %q %q %q %v", goal, approved, branch, state, baseline)
	}
	if _, err = db.Exec("PRAGMA foreign_keys=ON; INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('upstream/r',1,'s','','')"); err != nil {
		t.Fatal("a repository no room lists:", err)
	}
}
