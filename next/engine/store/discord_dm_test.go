package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestDiscordDMRoutePersistsAndDoesNotFollowRemapping(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, "other", "other", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiscordRoute(ctx, DiscordRoute{Guild: "100", Room: "room"}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDiscordDMRoute(ctx, "500", "100", "room"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	routes, err := s.DiscordDMRoutes(ctx, "room")
	if err != nil || routes["500"] != "100" {
		t.Fatal(routes, err)
	}
	routes, err = s.DiscordDMRoutes(ctx, "other")
	if err != nil || len(routes) != 0 {
		t.Fatal(routes, err)
	}
	if err := s.SetDiscordRoute(ctx, DiscordRoute{Guild: "100", Room: "other"}, false); err != nil {
		t.Fatal(err)
	}
	for _, room := range []contract.RoomID{"room", "other"} {
		routes, err = s.DiscordDMRoutes(ctx, room)
		if err != nil || len(routes) != 0 {
			t.Fatal(routes, err)
		}
	}
}
