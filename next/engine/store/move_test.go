package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestMoveRejectsDescendant(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,1,0);
 INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('p','r','p',NULL,'p','open','',0),('c','r','c','p','p','open','',0);
 INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('bp','p','h','image','agent','ready','','',0,0),('bc','c','h','image','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "move", Room: "r", Time: time.Unix(1, 0), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionMoved{Session: "p", NewParent: "c"}}
	err = s.MoveSession(ctx, "p", "c", event, 0)
	if err == nil {
		t.Fatal("move into descendant succeeded")
	}
	var parent *string
	if err := s.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id='p'").Scan(&parent); err != nil || parent != nil {
		t.Fatalf("parent changed: %v %v", parent, err)
	}
}
