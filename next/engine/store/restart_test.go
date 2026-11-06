package store

import (
	"context"
	"database/sql"
	"strings"
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
	if _, err := s.writer.Exec("UPDATE boxes SET create_retry_host='bad' WHERE id='b'"); err != nil {
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
	if rows[0].Host != "" || rows[0].RetryHost != "" {
		t.Fatalf("restart retained host or used retry budget: %+v", rows[0])
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

// A create that finds the host without its shared folder counts against the host at once, so the failed box leaves it on
// restart and a box that built nothing there places elsewhere. A box with files on the host is not moved.
func TestShareDownQuarantinesTheHostForABoxThatBuiltNothing(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('bad','','ready',8,8589934592,107374182400,0),('good','','ready',8,8589934592,107374182400,0),('other','','ready',8,8589934592,107374182400,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('s','r','s',NULL,'p','open','',0),('w','r','w',NULL,'p','open','',0),('f','r','f',NULL,'p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','bad','','agent','failed','create_failed','slice',0,0),('waiting','w','bad','','agent','pending','create_retry','',0,0),('built','f','other','image','agent','failed','create_failed','slice',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, contract.Event{ID: "waiting-initial", Room: "r", Time: time.UnixMilli(1), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "waiting", Session: "w", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineShare(ctx, box.Box{ID: "built", Session: "f", Host: "other", Image: "image", Size: size}); err != nil {
		t.Fatal(err)
	}
	if err := s.QuarantineShare(ctx, box.Box{ID: "b", Session: "s", Host: "bad", Size: size}); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if want := map[contract.HostName]contract.HostProblem{"bad": contract.HostProblemShareUnavailable}[h.Name]; h.Problem != want {
			t.Fatalf("host %s problem %q, want %q", h.Name, h.Problem, want)
		}
	}
	var waiting sql.NullString
	if err := s.Read().QueryRow("SELECT host FROM boxes WHERE id='waiting'").Scan(&waiting); err != nil || waiting.Valid {
		t.Fatalf("a box that built nothing kept the host: %v %v", waiting, err)
	}
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(200), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
	notice := contract.Event{ID: "n", Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: "s"}}
	if ok, err := s.RestartSession(ctx, "s", event, notice); err != nil || !ok {
		t.Fatalf("restart: %v %v", ok, err)
	}
	var host sql.NullString
	if err := s.Read().QueryRow("SELECT host FROM boxes WHERE id='b'").Scan(&host); err != nil || host.Valid {
		t.Fatalf("restart kept the host: %v %v", host, err)
	}
	if err := s.Read().QueryRow("SELECT host FROM boxes WHERE id='built'").Scan(&host); err != nil || host.String != "other" {
		t.Fatalf("a box with files moved: %v %v", host, err)
	}
}

// A successful create, such as a CI runner's, proves nothing about FUSE, so it leaves share_unavailable alone.
func TestASuccessfulCreateKeepsShareUnavailable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,create_problem) VALUES('h','','ready',8,8589934592,107374182400,0,'share_unavailable');
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('s','r','s',NULL,'p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','','agent','creating','','slice',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if err := s.FinishCreate(ctx, box.Box{ID: "b", Session: "s", Host: "h", Size: size, State: contract.BoxCreating}, "runner", "", time.Time{}, 0, event, false); err != nil {
		t.Fatal(err)
	}
	var problem string
	if err := s.Read().QueryRow("SELECT create_problem FROM hosts WHERE name='h'").Scan(&problem); err != nil || problem != "share_unavailable" {
		t.Fatalf("problem %q: %v", problem, err)
	}
}

// Restarting a failed box wakes its session when the session holds an open goal, and the stored notice says so.
func TestRestartWakesOnlyForAnOpenGoal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,1,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('work','r','work',NULL,'p','open','',0),('idle','r','idle',NULL,'p','open','',0);
INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',1,'work','g','d','active',0,0),('r',2,'idle','g','d','done',0,0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want,due_at,failures) VALUES('bw','work','h','image','agent','failed','error','slice',0,0,'stop',100,3),('bi','idle','h','image','agent','failed','error','slice',0,0,'stop',100,3)`)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[contract.SessionID]bool{"work": true, "idle": false} {
		event := contract.Event{ID: contract.EventID("restart-" + id), Room: "r", Time: time.UnixMilli(200), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
		notice := contract.Event{ID: contract.EventID("notice-" + id), Room: "r", Time: event.Time, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: id}}
		if ok, err := s.RestartSession(ctx, id, event, notice); err != nil || !ok {
			t.Fatalf("%s restart: %v %v", id, ok, err)
		}
		var wake bool
		if err := s.Read().QueryRowContext(ctx, `SELECT wake FROM deliveries WHERE to_id=?`, id).Scan(&wake); err != nil || wake != want {
			t.Fatalf("%s: wake %v, want %v: %v", id, wake, want, err)
		}
		stored, err := s.Events(ctx, "r", 0, 10, []contract.EventType{"session.restarted"})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range stored {
			if body := e.Body.(contract.SessionRestarted); body.Session == id && body.OpenGoal != want {
				t.Fatalf("%s: stored notice %+v", id, body)
			}
		}
		if open, err := s.HasOpenGoal(ctx, id); err != nil || open != want {
			t.Fatalf("%s: HasOpenGoal %v: %v", id, open, err)
		}
	}
}

// A refresh restart always wakes, records the open goal read in its own transaction, and a closed session's goal is not open.
func TestRefreshedNoticeWakesAndRecordsTheGoal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES('work','r','work',NULL,'p','open','',0,NULL),('idle','r','idle',NULL,'p','open','',0,NULL),('gone','r','gone',NULL,'p','open','',0,1);
INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',1,'work','g','d','active',0,0),('r',2,'gone','g','d','active',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	for id, goal := range map[contract.SessionID]bool{"work": true, "idle": false, "gone": false} {
		notice := contract.Event{ID: contract.EventID("n-" + id), Room: "r", Time: time.UnixMilli(200), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: id, Refreshed: true, ClientID: "refresh:" + string(id)}}
		for range 2 {
			if err := s.NoticeRestarted(ctx, notice); err != nil {
				t.Fatal(err)
			}
		}
		var n, wakes int
		if err := s.Read().QueryRowContext(ctx, `SELECT count(*), coalesce(sum(wake),0) FROM deliveries WHERE to_id=?`, id).Scan(&n, &wakes); err != nil || n != 1 || wakes != 1 {
			t.Fatalf("%s: deliveries %d wakes %d: %v", id, n, wakes, err)
		}
		var body string
		if err := s.Read().QueryRowContext(ctx, `SELECT CAST(body AS TEXT) FROM events WHERE client_id=?`, "refresh:"+string(id)).Scan(&body); err != nil || strings.Contains(body, `"openGoal":true`) != goal {
			t.Fatalf("%s: stored %s: %v", id, body, err)
		}
	}
}
