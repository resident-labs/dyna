package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRestartTreeAccess(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: worker})
	if e != nil {
		t.Fatal(e)
	}
	peer, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "peer", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='failed' WHERE session=?", child.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	req := contract.SessionRestartRequest{Session: child.Session}
	for _, caller := range []contract.Principal{
		{Kind: contract.PrincipalSession, Session: child.Session},
		{Kind: contract.PrincipalSession, Session: peer.Session},
		{Kind: contract.PrincipalEngine},
	} {
		if _, e := s.SessionRestart(ctx, caller, req); e == nil || e.Code != contract.ErrForbidden {
			t.Fatalf("caller %+v: %v", caller, e)
		}
	}
	if _, e := s.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, req); e != nil {
		t.Fatalf("ancestor: %v", e)
	}
}

func TestRoomReposSessionAccess(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, e := s.RoomRepos(ctx, caller, contract.RoomReposRequest{Room: room}); e != nil {
		t.Fatalf("own room: %v", e)
	}
	if _, e := s.RoomRepos(ctx, caller, contract.RoomReposRequest{Room: other.Room}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("other room: %v", e)
	}
	if _, e := s.RoomRepos(ctx, contract.Principal{Kind: contract.PrincipalEngine}, contract.RoomReposRequest{Room: room}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("engine: %v", e)
	}
	if _, e := s.RoomRepos(ctx, owner, contract.RoomReposRequest{Room: other.Room}); e != nil {
		t.Fatalf("owner: %v", e)
	}
}
