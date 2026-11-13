package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestInputBatchUpgradeUnreadAssignedOwner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('active','s',999000,'','',0,0,0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Write(ctx, func(tx *sql.Tx) error {
		seq, e := s.AppendEvent(tx, contract.Event{ID: "started", Room: "r", Time: now.Add(-time.Second), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{Session: "s", Turn: "active"}})
		if e != nil {
			return e
		}
		_, e = tx.Exec("UPDATE turns SET started_seq=? WHERE id='active'", seq)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	ev := contract.Event{ID: "owner", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "unread owner steer"}}
	if err = s.Write(ctx, func(tx *sql.Tx) error {
		seq, e := s.AppendEvent(tx, ev)
		if e != nil {
			return e
		}
		return s.InsertDelivery(ctx, tx, seq, ev.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Write(ctx, func(tx *sql.Tx) error {
		e := ev
		e.ID = "already-read"
		seq, err := s.AppendEvent(tx, e)
		if err != nil {
			return err
		}
		if err = s.InsertDelivery(ctx, tx, seq, e.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: false}); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE seq=?", seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.Exec("UPDATE deliveries SET state='taken',turn='active',queue='steer' WHERE state='pending'; DROP INDEX deliveries_open; ALTER TABLE deliveries DROP COLUMN taken_at; ALTER TABLE turns DROP COLUMN model_sent_at; ALTER TABLE deliveries DROP COLUMN immediate; DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='99' WHERE key='schema_version'"); err != nil {
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
	var readImmediate bool
	if err = s.Read().QueryRowContext(ctx, "SELECT immediate FROM deliveries WHERE state='read'").Scan(&readImmediate); err != nil || readImmediate {
		t.Fatalf("migration changed read history: %v %v", readImmediate, err)
	}
	end := contract.Event{ID: "ended", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Body: contract.TurnEnded{Session: "s", Turn: "active", Outcome: contract.TurnStopped}}
	if err = s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "active", Outcome: contract.TurnStopped}, end); err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.Dispatch(ctx, "s", "next", contract.Event{ID: "next", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if turn != "next" {
		deadline, e := s.InputBatchDeadline(ctx, "s")
		t.Fatalf("legacy unread assigned OWNER was delayed: turn=%q deadline=%v err=%v", turn, deadline, e)
	}
}
