package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestLostHelloIgnoresInventoryDeadline(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,5,10);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,due_at) VALUES('b','s','h','i','agent','lost','','',0,0,50)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	old := box.Box{ID: "b", Session: "s", Host: "h", Size: size, State: contract.BoxLost}
	event := contract.Event{ID: "hello", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := s.TransitionBox(ctx, old, contract.BoxReady, "", event); err != nil {
		t.Fatalf("lost hello: %v", err)
	}
	var due int64
	if err := s.Read().QueryRow("SELECT due_at FROM boxes WHERE id='b'").Scan(&due); err != nil || due != 0 {
		t.Fatalf("due %d: %v", due, err)
	}
}

func TestHostLossGuardsStaleResults(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','unreachable',1,1,5,10),('drain','','draining',1,1,5,10);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','i','agent','lost','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	old := box.Box{ID: "b", Session: "s", Host: "h", Size: size, State: contract.BoxLost}
	next := old
	next.State = contract.BoxStopped
	event := contract.Event{ID: "stale", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if ok, err := s.SetBoxLifecycle(ctx, old, next, time.Time{}, 0, event); err != nil || ok {
		t.Fatalf("stale inventory: %v %v", ok, err)
	}
	if ok, err := s.SetBoxLifecycle(ctx, old, old, time.UnixMilli(200), 0, event); err != nil || ok {
		t.Fatalf("stale due update: %v %v", ok, err)
	}
	if _, err := s.TransitionBox(ctx, old, contract.BoxReady, "", event); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale hello: %v", err)
	}
	if err := s.RecordHostHeartbeat(ctx, "h", true, nil, 0, time.UnixMilli(50)); err != nil {
		t.Fatal(err)
	}
	var state string
	var disk, checked int64
	if err := s.Read().QueryRow("SELECT state,disk_bytes,checked_at FROM hosts WHERE name='h'").Scan(&state, &disk, &checked); err != nil || state != "ready" || disk != 5 || checked != 10 {
		t.Fatalf("empty sample %s %d %d: %v", state, disk, checked, err)
	}
	sample := int64(42)
	if err := s.RecordHostHeartbeat(ctx, "drain", true, &sample, 0, time.UnixMilli(60)); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT state,disk_bytes,checked_at FROM hosts WHERE name='drain'").Scan(&state, &disk, &checked); err != nil || state != "draining" || disk != 42 || checked != 60 {
		t.Fatalf("draining sample %s %d %d: %v", state, disk, checked, err)
	}
	if _, err := s.writer.Exec(`UPDATE boxes SET want='discard' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	ready := old
	ready.State = contract.BoxReady
	if ok, err := s.SetBoxLifecycle(ctx, old, ready, time.Time{}, 0, event); err != nil || ok {
		t.Fatalf("reconnect after discard: %v %v", ok, err)
	}
	if ok, err := s.SetBoxLifecycle(ctx, old, next, time.Time{}, 0, event); err != nil || !ok {
		t.Fatalf("fresh inventory: %v %v", ok, err)
	}
}
