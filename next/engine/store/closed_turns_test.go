package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestClosedTurnsMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at,closed_at) VALUES('closed','r','closed','p','closed','',1,777),('live','r','live','p','open','',1,NULL),('closing','r','closing','p','closing','',1,NULL);
 INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('repair','closed',2,NULL,'','',0,0,0,0),('ended','closed',2,555,'failed','',0,0,0,0),('live','live',2,NULL,'','',0,0,0,0),('closing','closing',2,NULL,'','',0,0,0,0);
 DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE rooms DROP COLUMN git_signing; ALTER TABLE sessions DROP COLUMN advisor; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='102' WHERE key='schema_version';`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		id      string
		ended   int64
		outcome string
	}{{"repair", 777, "closed"}, {"ended", 555, "failed"}, {"live", 0, ""}, {"closing", 0, ""}} {
		var ended sql.NullInt64
		var outcome string
		if err = s.Read().QueryRow("SELECT ended_at,outcome FROM turns WHERE id=?", tc.id).Scan(&ended, &outcome); err != nil {
			t.Fatal(err)
		}
		if ended.Valid != (tc.ended != 0) || ended.Int64 != tc.ended || outcome != tc.outcome {
			t.Fatalf("%s: %v %s", tc.id, ended, outcome)
		}
	}
}

func TestCloseSessionEndsOnlyOpenTurns(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	_, err := s.writer.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('previous','child',1,2,'failed','',0,0,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(9999)
	if err = s.CloseSession(context.Background(), "child", contract.Event{ID: "close", Time: at, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionClosed{Session: "child"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id      string
		at      int64
		outcome contract.TurnOutcome
	}{{"turn", 9999, contract.TurnClosed}, {"previous", 2, contract.TurnFailed}} {
		var ended int64
		var outcome contract.TurnOutcome
		if err = s.Read().QueryRow("SELECT ended_at,outcome FROM turns WHERE id=?", tc.id).Scan(&ended, &outcome); err != nil || ended != tc.at || outcome != tc.outcome {
			t.Fatalf("%s: %d %s %v", tc.id, ended, outcome, err)
		}
	}
}
