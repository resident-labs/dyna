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
 DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; UPDATE meta SET value='101' WHERE key='schema_version';`)
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
