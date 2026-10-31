package store

import (
	"context"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
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
