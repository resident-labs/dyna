package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestControlsEnforceAuthority(t *testing.T) {
	s, room, worker, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, problem := s.RoomPause(ctx, caller, contract.RoomPauseRequest{Room: room, Paused: true}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker paused room: %v", problem)
	}
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.SessionStop(ctx, caller, contract.SessionStopRequest{Session: root}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker stopped ancestor: %v", problem)
	}
}
