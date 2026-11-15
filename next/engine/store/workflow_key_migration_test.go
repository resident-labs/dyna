package store

import (
	"database/sql"
	"testing"
)

func TestWorkflowKeyRenameKeepsStateQueueAndOrigin(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		"CREATE TABLE workflow_runs (sub TEXT,subject TEXT)",
		"INSERT INTO workflow_runs VALUES('review','owner/repo#7')",
		"CREATE TABLE workflow_queue (sub TEXT,subject TEXT,body TEXT)",
		"INSERT INTO workflow_queue VALUES('review','owner/repo#7','event')",
		"CREATE TABLE workflow_state (sub TEXT,subject TEXT,value TEXT, PRIMARY KEY(sub,subject))",
		`INSERT INTO workflow_state VALUES('review','owner/repo#7','{"head":"abc"}')`,
		"CREATE TABLE workflow_subscriptions (sub TEXT,subject_key TEXT,once_only INTEGER,report TEXT)",
		"INSERT INTO workflow_subscriptions VALUES('review','{repo}#{number}',1,'root')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range steps[114] {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"workflow_runs", "workflow_queue", "workflow_state"} {
		var key string
		if err := db.QueryRow("SELECT key FROM " + table + " WHERE sub='review'").Scan(&key); err != nil {
			t.Fatal(err)
		}
		if key != "owner/repo#7" {
			t.Fatalf("%s key %q", table, key)
		}
	}
	var key, parent, value, body string
	var latest bool
	if err := db.QueryRow("SELECT key,latest,parent FROM workflow_subscriptions").Scan(&key, &latest, &parent); err != nil {
		t.Fatal(err)
	}
	if key != "{repo}#{number}" || !latest || parent != "root" {
		t.Fatal("renaming changed subscription settings")
	}
	if err := db.QueryRow("SELECT value FROM workflow_state").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != `{"head":"abc"}` {
		t.Fatal("state changed")
	}
	if err := db.QueryRow("SELECT body FROM workflow_queue").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "event" {
		t.Fatal("pending event changed")
	}
}
