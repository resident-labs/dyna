package store

import (
	"context"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"reflect"
	"testing"
	"time"
)

func TestHostRoomsPersist(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "restricted", Rooms: []contract.RoomID{"room"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "open"}); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || len(hosts[0].Rooms) != 0 || len(hosts[1].Rooms) != 1 || hosts[1].Rooms[0] != "room" {
		t.Fatalf("hosts: %+v", hosts)
	}
}

func TestHostSessionsListLiveBoxesByHost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r1','r1',0,0,0),('r2','r2',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('a','','ready',1,1,1,0),('b','','ready',1,1,1,0),('idle','','ready',1,1,1,0);
 INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES
  ('later','r1','Later',NULL,'p','open','',2),('first','r1','First',NULL,'p','open','',1),('asleep','r2','Asleep',NULL,'p','open','',3),
  ('other','r1','Other',NULL,'p','open','',4),('gone','r1','Gone',NULL,'p','open','',5),('waiting','r1','Waiting',NULL,'p','open','',6);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES
  ('b1','later','a','i','agent','ready','','',0,0),('b2','first','a','i','agent','closing','','',0,0),('b3','asleep','a','i','agent','stopped','','',0,0),
  ('b4','other','b','i','agent','failed','','',0,0),('b5','gone','a','i','agent','deleted','','',0,0),('b6','waiting',NULL,'','agent','pending','','',0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.HostSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[contract.HostName][]contract.HostSession{
		"a": {{Room: "r1", ID: "first", Name: "First", BoxState: contract.BoxClosing}, {Room: "r1", ID: "later", Name: "Later", BoxState: contract.BoxReady}, {Room: "r2", ID: "asleep", Name: "Asleep", BoxState: contract.BoxStopped}},
		"b": {{Room: "r1", ID: "other", Name: "Other", BoxState: contract.BoxFailed}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions: %+v", got)
	}
	list, err := s.SessionList(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	hosts := make(map[contract.SessionID]contract.HostName)
	for _, session := range list.Sessions {
		hosts[session.ID] = session.Host
	}
	// A deleted box keeps its host in the table, and a box not placed yet has none; neither names a machine.
	if want := map[contract.SessionID]contract.HostName{"later": "a", "first": "a", "other": "b", "gone": "", "waiting": ""}; !reflect.DeepEqual(hosts, want) {
		t.Fatalf("session.list hosts: %+v", hosts)
	}
}
