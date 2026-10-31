package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestGrantRoundTripAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0).UTC()
	if err = s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	g := contract.Grant{ID: "g", Kind: contract.GrantHostExec, Room: "room", To: who, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now, Expires: now.Add(time.Hour), Mode: contract.GrantModeApprove, Hosts: []contract.HostName{"a", "b"}, Commands: []contract.CommandRule{{Program: "git", Args: []string{"status", "-s"}}, {Program: "sh", Args: []string{"-c"}, Prefix: true}}}
	if err = s.Write(ctx, func(tx *sql.Tx) error { return s.InsertGrant(tx, g) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListActiveGrants(ctx, "room", who, now)
	if err != nil || len(got) != 1 || len(got[0].Hosts) != 2 || len(got[0].Commands) != 2 || len(got[0].Commands[0].Args) != 2 || got[0].Commands[1].Args[0] != "-c" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if err = s.RevokeGrant(ctx, g.ID, now); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListActiveGrants(ctx, "room", who, now)
	if err != nil || len(got) != 0 {
		t.Fatalf("revoked grant remains active: %+v %v", got, err)
	}
	if err = s.RevokeGrant(ctx, g.ID, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second revoke: %v", err)
	}
	g.ID = "expiry"
	if err = s.Write(ctx, func(tx *sql.Tx) error { return s.InsertGrant(tx, g) }); err != nil {
		t.Fatal(err)
	}
	due := now.Add(time.Hour)
	event := func(room contract.RoomID, id contract.GrantID) (contract.Event, error) {
		return contract.Event{ID: "e", Time: due, Room: room, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.GrantExpired{Grant: id}}, nil
	}
	for i := 0; i < 2; i++ {
		if err = s.ExpireGrants(ctx, due, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events(ctx, "room", 0, 10, nil)
	if err != nil || len(events) != 1 || events[0].Body.EventType() != "grant.expired" {
		t.Fatalf("events: %+v %v", events, err)
	}
}
