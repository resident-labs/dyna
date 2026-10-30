package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestClosingLeaf(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES ('root','room','root',NULL,'p','closing','',0,NULL),('parent','room','parent','root','p','closing','',0,NULL),('leaf','room','leaf','parent','p','closing','',0,NULL),('closed','room','closed','leaf','p','closed','',0,1),('ready','room','ready','root','p','ready','',0,NULL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id   contract.SessionID
		want bool
	}{{"root", false}, {"parent", false}, {"leaf", true}, {"closed", false}, {"ready", false}, {"missing", false}} {
		got, err := s.ClosingLeaf(ctx, tt.id)
		if err != nil || got != tt.want {
			t.Fatalf("%s: got %v, error %v", tt.id, got, err)
		}
	}
}

func TestRoomTree(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES ('root','room','root',NULL,'p','ready','',0,NULL),('child','room','child','root','p','ready','',0,NULL),('newroot','room','newroot',NULL,'p','ready','',1,NULL),('closed','room','closed','root','p','closed','',0,1)")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := s.RoomTree(ctx, "room")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tree.ParentFor(contract.Principal{Kind: contract.PrincipalOwner}, "")
	if err != nil || parent != "root" || !tree.Above("root", "child") || tree.HoldsGoals("closed") {
		t.Fatalf("parent %q, error %v", parent, err)
	}
}
