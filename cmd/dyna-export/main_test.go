package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestExportAndVerifyCapturedInputs(t *testing.T) {
	dir := t.TempDir()
	data, config, backup := filepath.Join(dir, "data"), filepath.Join(dir, "config"), filepath.Join(dir, "backup.db")
	roomID := "room_20260101-000000-aabbccdd"
	for _, p := range []string{filepath.Join(data, "rooms", roomID), config} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(config, "settings.json"):                 `{}`,
		filepath.Join(config, "profiles.json"):                 `{"version":3,"profiles":[]}`,
		filepath.Join(data, "rooms", roomID, "messages.jsonl"): "",
		filepath.Join(dir, "manifest.json"):                    `{"repositories":[{"path":"/capture/repo","identity":"github.com/acme/fork","defaultRoom":"alpha"}],"archiveRefs":[],"skills":[]}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT);
INSERT INTO meta VALUES ('room.records','1'),('room.records.import','1'),('goals.schema','5'),('goal_items.schema','2'),('questions.schema','4'),('room.issues.schema','3'),('sessions.records','7'),('sessions.import','1'),('machines.version','18'),('account.registry','11'),('web_devices.version','1'),('device_grants.version','2'),('hooks.documents','5'),('pr.state','2'),('pr.record_parts','1'),('localci.schema','6'),('sessions.service','1'),('sessions.history','1');
CREATE TABLE room_records(id TEXT,body TEXT);
CREATE TABLE goal_rooms(room TEXT,body TEXT);
CREATE TABLE goals(room TEXT,id TEXT,state TEXT,body TEXT);
CREATE TABLE goal_items(room TEXT,goal TEXT,id TEXT,text TEXT,state TEXT,assignee TEXT);
CREATE TABLE room_issues(seq INTEGER,room_id TEXT,id TEXT,state TEXT,body TEXT);
CREATE TABLE room_issue_numbers(room_id TEXT,number INTEGER,message_id TEXT);
CREATE TABLE questions(key TEXT,room TEXT,state TEXT,body TEXT);
CREATE TABLE pr_rows(room TEXT,repo TEXT,number INTEGER,closed INTEGER,body TEXT);
CREATE TABLE session_records(id TEXT,room_id TEXT,room TEXT,name TEXT,parent TEXT,closed INTEGER,history_only INTEGER,service INTEGER,meta TEXT);
CREATE TABLE machines(name TEXT,ssh TEXT,enabled INTEGER,local INTEGER,labels TEXT,rooms TEXT);
CREATE TABLE accounts(id TEXT,ord INTEGER,metadata TEXT);
CREATE TABLE account_repo_scopes(account_id TEXT,repo TEXT);
CREATE TABLE web_devices(id TEXT,name TEXT,hash TEXT,created TEXT,expires TEXT);
CREATE TABLE device_grants(kind TEXT,id TEXT,scopes TEXT);
CREATE TABLE hook_documents(repo TEXT,body TEXT);
CREATE TABLE local_ci_modes(room_id TEXT,mode TEXT);
INSERT INTO room_records VALUES (?,?);
INSERT INTO goals VALUES (?,'g1','active','{"id":"g1","text":"Goal","setter":"owner"}');
INSERT INTO goal_items VALUES (?,'g1','i1','Item','open','');`, roomID, `{"id":"`+roomID+`","name":"alpha","repo":"/capture/repo","upstream":"github.com/acme/upstream","rootProfile":"sonnet","sessionPause":{"paused":true}}`, roomID, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--data", data, "--feeds", data, "--config", config, "--backup", backup, "--manifest", filepath.Join(dir, "manifest.json"), "--out", filepath.Join(dir, "out")}
	if err := run(append(append([]string{}, args...), "--commit", strings.Repeat("a", 40))); err != nil {
		t.Fatal(err)
	}
	if err := run(append([]string{"verify"}, args...)); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "out", "rooms", "alpha.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	part, err := contract.DecodeBundle[contract.RoomBundle](f)
	if err != nil {
		t.Fatal(err)
	}
	if !part.Paused || part.RootProfile != "sonnet" || len(part.Upstreams) != 1 || part.Upstreams[0].Upstream != "github.com/acme/upstream" || part.Upstreams[0].Repo != "acme/fork" {
		t.Fatalf("room handoff fields missing: %+v", part)
	}
}
