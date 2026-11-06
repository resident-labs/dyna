package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestCreateQuarantine(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',4,10000000000,100000000000,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','','agent','creating','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	b := box.Box{ID: "b", Session: "s", Host: "h", Size: size, State: contract.BoxCreating}
	event := func(i int) contract.Event {
		return contract.Event{ID: contract.EventID(fmt.Sprintf("e%d", i)), Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Cause: contract.CauseCreateFailed}}
	}
	if err := s.FinishCreate(ctx, b, "", "retry", time.Time{}, 1, event(9), false); err != nil {
		t.Fatal(err)
	}
	var retries int
	if err := s.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='h'").Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("attempt counted: %d %v", retries, err)
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='creating',host='h',create_retry_host='' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCreate(ctx, b, "", "image build failed", time.Time{}, 3, event(10), false); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='h'").Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("image failure counted: %d %v", retries, err)
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='creating',host='h',create_retry_host='' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.FinishCreate(ctx, b, "", "failure", time.Time{}, 3, event(i), true); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if _, err := s.writer.Exec("UPDATE boxes SET state='creating',host='h',create_retry_host='' WHERE id='b'"); err != nil {
				t.Fatal(err)
			}
		}
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Problem != contract.HostProblemCreateFailed {
		t.Fatalf("quarantine: %+v %v", hosts, err)
	}
	if err := s.RecordHostHeartbeat(ctx, "h", true, nil, 0, time.UnixMilli(101)); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.Hosts(ctx)
	if hosts[0].Problem == "" {
		t.Fatal("heartbeat cleared problem")
	}
	if got := box.Admit(hosts, nil, box.Box{ID: "new", Room: "r", Size: size}); got.Host != "" {
		t.Fatalf("admitted quarantined host: %+v", got)
	}
	// A stale placement snapshot cannot admit a new creation after quarantine.
	if _, err := s.writer.Exec("UPDATE boxes SET state='pending' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	pending := b
	pending.State = contract.BoxPending
	if ok, err := s.SetBoxLifecycle(ctx, pending, b, time.Time{}, 0, event(8)); err != nil || ok {
		t.Fatalf("stale admission: %v %v", ok, err)
	}
	// A successful creation already in flight is stronger evidence than a heartbeat.
	if _, err := s.writer.Exec("UPDATE boxes SET state='creating',host='h',create_retry_host='' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCreate(ctx, b, "image", "", time.Time{}, 0, event(3), false); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.Hosts(ctx)
	if hosts[0].Problem != "" {
		t.Fatal("in-flight success did not clear problem")
	}
	if _, err := s.writer.Exec("UPDATE hosts SET create_failures=3,create_problem='create_failed' WHERE name='h'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='creating',host='h',create_retry_host='' WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostSwitches(ctx, "h", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCreate(ctx, b, "", "failure", time.Time{}, 3, event(4), true); err != nil {
		t.Fatal(err)
	}
	var count int
	var problem string
	if err := s.Read().QueryRow("SELECT create_failures,create_problem FROM hosts WHERE name='h'").Scan(&count, &problem); err != nil || count != 0 || problem != "" {
		t.Fatalf("stale result after clear: %d %q %v", count, problem, err)
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='starting',create_generation=(SELECT create_generation FROM hosts WHERE name='h') WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	missing := event(5)
	missing.Body = contract.BoxChanged{Cause: contract.CauseKVMDeviceMissing}
	if err := s.FinishCreate(ctx, b, "", "KVM unavailable", time.Time{}, 0, missing, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='h'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing device counted: %d %v", count, err)
	}
}

// A host that becomes quarantined stops keeping boxes that wait to retry with nothing built; they place elsewhere.
func TestQuarantineDetachesWaitingBoxes(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,create_failures) VALUES('h','','ready',4,10000000000,100000000000,0,2);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0),('w','r','w','p','open','',0),('k','r','k','p','open','',0),('p','r','p','p','open','',0);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','','agent','creating','','',0,0),('wait','w','h','','agent','pending','create_retry','sw',0,0),('built','k','h','img','agent','pending','','sk',0,0),('pin','p','h','','agent','pending','create_retry','sp',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	b := box.Box{ID: "b", Session: "s", Host: "h", Size: size, State: contract.BoxCreating}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		for _, initial := range []contract.BoxChanged{{Box: "wait", Session: "w", To: contract.BoxPending}, {Box: "pin", Session: "p", Host: "h", To: contract.BoxPending}} {
			if _, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID("initial-" + string(initial.Box)), Room: "r", Time: time.UnixMilli(99), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: initial}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Cause: contract.CauseCreateFailed}}
	if err := s.FinishCreate(ctx, b, "", "failure", time.Time{}, 3, event, true); err != nil {
		t.Fatal(err)
	}
	var waiting, built, pinned sql.NullString
	if err := s.Read().QueryRow("SELECT (SELECT host FROM boxes WHERE id='wait'),(SELECT host FROM boxes WHERE id='built'),(SELECT host FROM boxes WHERE id='pin')").Scan(&waiting, &built, &pinned); err != nil {
		t.Fatal(err)
	}
	if waiting.Valid || built.String != "h" || pinned.String != "h" {
		t.Fatalf("waiting host %v, built host %v", waiting, built)
	}
	leftovers, err := s.Leftovers(ctx, "h")
	if err != nil || len(leftovers) != 1 || leftovers[0].Box != "wait" {
		t.Fatalf("leftovers %+v: %v", leftovers, err)
	}
}
