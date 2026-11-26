package store

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The advisor setting moved from a session to a room column: an upgrade copies each open advisor session's profile into it, and leaves a room
// with a closed one, or none, on no advisor.
func TestAdvisorProfileMigrationCopiesTheOpenAdvisorSessionsProfile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('with','with',0,0,0),('closed','closed',0,0,0),('none','none',0,0,0);
ALTER TABLE sessions ADD COLUMN advisor INTEGER NOT NULL DEFAULT 0;
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at,advisor) VALUES('a1','with','advisor','deep','open','',0,1),('a2','closed','advisor','deep','open','',0,1),('r','none','root','test','open','',0,0);
 UPDATE sessions SET closed_at=1 WHERE id='a2';
 ALTER TABLE rooms DROP COLUMN advisor_profile; DROP TABLE agents; DROP INDEX workflow_runs_service; DROP TABLE workflow_streaks; ALTER TABLE workflow_runs DROP COLUMN served; DROP INDEX workflow_queue_key; ALTER TABLE workflow_queue DROP COLUMN boot; ALTER TABLE workflow_queue DROP COLUMN handovers; ALTER TABLE workflow_steps DROP COLUMN event; ALTER TABLE workflow_steps DROP COLUMN progress_offset; ALTER TABLE workflow_steps DROP COLUMN tool_calls; ALTER TABLE workflow_steps DROP COLUMN last_activity; ALTER TABLE room_repos DROP COLUMN ci_hosts; ALTER TABLE workflow_steps DROP COLUMN raw_text; DROP TABLE browsers; ALTER TABLE rooms DROP COLUMN browser; DROP TABLE service_feed_messages; DROP TABLE service_outbox; ALTER TABLE services DROP COLUMN feed_in; ALTER TABLE services DROP COLUMN feed_out; ALTER TABLE services DROP COLUMN feed_last_at; ALTER TABLE turns DROP COLUMN profile; ALTER TABLE boxes DROP COLUMN can_handoff; ALTER TABLE boxes DROP COLUMN hello_seq; DROP TABLE agy_streak; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN agy_account; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at; UPDATE meta SET value='120' WHERE key='schema_version'`)
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
	for room, want := range map[string]string{"with": "deep", "closed": "", "none": ""} {
		got, err := s.RoomAdvisorProfile(context.Background(), contract.RoomID(room))
		if err != nil || string(got) != want {
			t.Fatalf("room %s: advisor %q (%v), want %q", room, got, err, want)
		}
	}
}

// The advisor stopped being a session: an upgrade starts each open advisor session closing, with its box to close so the engine finishes it,
// and drops the flag. Other sessions, and an advisor already closed, are left alone.
func TestAdvisorSessionsStartClosingWhenTheFlagGoes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room','room',0,0,0);
 ALTER TABLE sessions ADD COLUMN advisor INTEGER NOT NULL DEFAULT 0;
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at,advisor) VALUES('open','room','advisor','deep','open','',0,1),('old','room','old','deep','closed','',0,1),('root','room','root','test','open','',0,0);
 UPDATE sessions SET closed_at=1 WHERE id='old';
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b-open','open',NULL,'','small','ready','','',0,0,'run'),('b-root','root',NULL,'','small','ready','','',0,0,'run');
 DROP INDEX workflow_runs_service; DROP TABLE workflow_streaks; ALTER TABLE workflow_runs DROP COLUMN served; DROP INDEX workflow_queue_key; ALTER TABLE workflow_queue DROP COLUMN boot; ALTER TABLE workflow_queue DROP COLUMN handovers; ALTER TABLE workflow_steps DROP COLUMN event; ALTER TABLE workflow_steps DROP COLUMN progress_offset; ALTER TABLE workflow_steps DROP COLUMN tool_calls; ALTER TABLE workflow_steps DROP COLUMN last_activity; ALTER TABLE room_repos DROP COLUMN ci_hosts; ALTER TABLE workflow_steps DROP COLUMN raw_text; DROP TABLE browsers; ALTER TABLE rooms DROP COLUMN browser; DROP TABLE service_feed_messages; DROP TABLE service_outbox; ALTER TABLE services DROP COLUMN feed_in; ALTER TABLE services DROP COLUMN feed_out; ALTER TABLE services DROP COLUMN feed_last_at; ALTER TABLE turns DROP COLUMN profile; ALTER TABLE boxes DROP COLUMN can_handoff; ALTER TABLE boxes DROP COLUMN hello_seq; DROP TABLE agy_streak; ALTER TABLE sessions DROP COLUMN handoff; ALTER TABLE sessions DROP COLUMN agy_account; ALTER TABLE sessions DROP COLUMN cleared_at; ALTER TABLE turns DROP COLUMN cleared_at; UPDATE meta SET value='121' WHERE key='schema_version'`)
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
	for session, want := range map[string][2]string{"open": {"closing", "close"}, "old": {"closed", ""}, "root": {"open", "run"}} {
		var state, box string
		if err := s.Read().QueryRow(`SELECT s.state,COALESCE((SELECT want FROM boxes WHERE session=s.id),'') FROM sessions s WHERE s.id=?`, session).Scan(&state, &box); err != nil || state != want[0] || box != want[1] {
			t.Fatalf("session %s: state %q, box %q (%v), want %v", session, state, box, err, want)
		}
	}
	if _, err := s.Read().Exec(`SELECT advisor FROM sessions`); err == nil {
		t.Fatal("the advisor flag is still a column")
	}
}
