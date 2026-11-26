package store

import (
	"testing"
)

func TestTimestampMigrationHistoricalZero(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('old','s',1,'','',0,0,0,0);
 ALTER TABLE turns DROP COLUMN model_sent_at; ALTER TABLE deliveries DROP COLUMN taken_at;
 DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE rooms DROP COLUMN advisor_profile; DROP TABLE agents; ALTER TABLE rooms DROP COLUMN git_signing; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; DROP INDEX workflow_runs_service; DROP TABLE workflow_streaks; ALTER TABLE workflow_runs DROP COLUMN served; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; ALTER TABLE workflow_steps DROP COLUMN event; ALTER TABLE workflow_steps DROP COLUMN progress_offset; ALTER TABLE workflow_steps DROP COLUMN tool_calls; ALTER TABLE workflow_steps DROP COLUMN last_activity; ALTER TABLE room_repos DROP COLUMN ci_hosts; ALTER TABLE workflow_steps DROP COLUMN raw_text; DROP TABLE browsers; ALTER TABLE rooms DROP COLUMN browser; DROP TABLE service_feed_messages; DROP TABLE service_outbox; ALTER TABLE services DROP COLUMN feed_in; ALTER TABLE services DROP COLUMN feed_out; ALTER TABLE services DROP COLUMN feed_last_at; ALTER TABLE boxes DROP COLUMN can_handoff; ALTER TABLE boxes DROP COLUMN hello_seq; DROP TABLE agy_streak; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN agy_account; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at; UPDATE meta SET value='101' WHERE key='schema_version';`)
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
	var indexSQL string
	if err = s.Read().QueryRow("SELECT sql FROM sqlite_master WHERE name='deliveries_open'").Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if indexSQL != steps[100][0] {
		t.Fatalf("timestamp migration changed open-delivery index: %s", indexSQL)
	}
	var model int64
	if err = s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='old'").Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != 0 {
		t.Fatalf("invented historical timestamp: %d", model)
	}
	for _, table := range []string{"deliveries", "turns"} {
		field := "taken_at"
		if table == "turns" {
			field = "model_sent_at"
		}
		var nonnull int
		var def string
		if err = s.Read().QueryRow("SELECT [notnull],dflt_value FROM pragma_table_info(?) WHERE name=?", table, field).Scan(&nonnull, &def); err != nil {
			t.Fatal(err)
		}
		if nonnull != 1 || def != "0" {
			t.Fatalf("%s boundary default: %d %s", field, nonnull, def)
		}
	}
}
