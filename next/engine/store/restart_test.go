package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestRestartOnQuarantinedHost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,create_problem) VALUES('bad','','ready',8,8589934592,107374182400,0,'create_failed'),('good','','ready',8,8589934592,107374182400,0,'');
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('s','r','s',NULL,'p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','bad','','agent','failed','create_failed','slice',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(200), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
	notice := contract.Event{ID: "n", Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: "s"}}
	if ok, err := s.RestartSession(ctx, "s", event, notice); err != nil || !ok {
		t.Fatalf("restart: %v %v", ok, err)
	}
	rows, err := s.LifecycleBoxes(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("boxes: %v %v", rows, err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Host != "" {
		t.Fatalf("restart retained quarantined host %q", rows[0].Host)
	}
	leftovers, err := s.Leftovers(ctx, "bad")
	if err != nil || len(leftovers) != 1 || leftovers[0].Box != "b" || leftovers[0].Slice != "slice" {
		t.Fatalf("old host cleanup: %+v %v", leftovers, err)
	}
	if got := box.Admit(hosts, []box.Box{rows[0].Box}, rows[0].Box); got.Host != "good" {
		t.Fatalf("placement: %+v", got)
	}
}

func TestRestartFailedSessions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,1,0),('down','','unreachable',1,1,1,0);
 INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','root goal',0),('child','r','child','root','p','open','child goal',0);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures) VALUES('br','root','h','image','agent','failed','error','slice',0,0,'stop',100,3),('bc','child','down','image','agent','failed','error','slice',0,0,'stop',100,3)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []contract.SessionID{"root", "child"} {
		event := contract.Event{ID: contract.EventID("restart-" + id), Room: "r", Time: time.UnixMilli(200), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
		notice := contract.Event{ID: contract.EventID("notice-" + id), Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: id}}
		ok, err := s.RestartSession(ctx, id, event, notice)
		if err != nil || !ok {
			t.Fatalf("%s restart: %v %v", id, ok, err)
		}
		var state, want, image, reason, goal string
		var host sql.NullString
		var due, failures int
		err = s.Read().QueryRowContext(ctx, `SELECT b.state,b.want,b.image,b.reason,b.host,b.due_at,b.failures,s.goal FROM boxes b JOIN sessions s ON s.id=b.session WHERE s.id=?`, id).Scan(&state, &want, &image, &reason, &host, &due, &failures, &goal)
		if err != nil || state != "pending" || want != "run" || image != "" || reason != "" || (id == "root" && (!host.Valid || host.String != "h")) || (id == "child" && host.Valid) || due != 0 || failures != 0 || goal != string(id)+" goal" {
			t.Fatalf("%s reset: %s %s %q %q %v %d %d %q: %v", id, state, want, image, reason, host, due, failures, goal, err)
		}
		dm, err := s.EventsPage(ctx, "r", 0, 0, 10, []contract.EventType{"session.restarted"}, id)
		if err != nil || len(dm) != 1 {
			t.Fatalf("restart DM %v: %v", dm, err)
		}
		ok, err = s.RestartSession(ctx, id, event, notice)
		if err != nil || ok {
			t.Fatalf("%s repeated restart: %v %v", id, ok, err)
		}
	}
	var pending int
	if err := s.Read().QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE wake=0 AND state='pending'`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("quiet restart deliveries %d: %v", pending, err)
	}
	events, err := s.Events(ctx, "r", 0, 10, []contract.EventType{"box.changed"})
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v: %v", events, err)
	}
	for _, event := range events {
		body := event.Body.(contract.BoxChanged)
		if body.From != contract.BoxFailed || body.To != contract.BoxPending {
			t.Fatalf("event %+v", body)
		}
	}
}
