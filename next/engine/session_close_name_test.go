package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestCloseListedSessionByName(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	started, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "build-gen", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	listed, problem := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	var name string
	for _, session := range listed.Sessions {
		if session.ID == started.Session {
			name = session.Name
		}
	}
	if name == "" {
		t.Fatal("session missing from list")
	}
	if _, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: string(started.Session), Profile: "test"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("ID-shaped name: %v", problem)
	}
	other, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: other.Room, Name: "elsewhere", Profile: "test"}); problem != nil {
		t.Fatal(problem)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: root}
	if _, problem := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: "elsewhere"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("other room name: %v", problem)
	}
	if _, problem := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: contract.SessionID(name)}); problem != nil {
		t.Fatalf("close listed session %q: %v", name, problem)
	}
	var state string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", started.Session).Scan(&state); err != nil || (state != "closing" && state != "closed") {
		t.Fatalf("session state %q: %v", state, err)
	}
}
