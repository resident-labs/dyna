package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

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
