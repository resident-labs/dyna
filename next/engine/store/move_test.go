package store

import (
	"context"
	"strings"
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

func TestMovePastTheDepthLimitNamesTheMove(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// root r0 > a > b, and root r0 > c > d: moving c under b puts d at depth 4.
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('r0','r','r0',NULL,'p','open','',0),('a','r','a','r0','p','open','',0),('b','r','b','a','p','open','',0),('c','r','c','r0','p','open','',0),('d','r','d','c','p','open','',0)`)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "move", Room: "r", Time: time.Unix(1, 0), From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.SessionMoved{Session: "c", NewParent: "b"}}
	err = s.MoveSession(ctx, "c", "b", event, 3)
	if err == nil || !strings.Contains(err.Error(), "past the room's limit of 3; choose a parent higher in the tree") {
		t.Fatalf("deep move: %v", err)
	}
}
