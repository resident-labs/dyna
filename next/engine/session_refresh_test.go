package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

var refreshOwner = contract.Principal{Kind: contract.PrincipalOwner}

func startChild(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, name string, parent contract.SessionID) contract.SessionID {
	t.Helper()
	child, problem := s.SessionStart(ctx, refreshOwner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if _, problem := s.BoxHello(ctx, boxCaller(child.Session), contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	return child.Session
}

func TestRefreshAccess(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	child := startChild(t, s, ctx, room, "child", sid)
	peer := startChild(t, s, ctx, room, "peer", "")
	other, problem := s.RoomCreate(ctx, refreshOwner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	single := contract.SessionRefreshRequest{Session: child}
	if _, problem := s.SessionRefresh(ctx, boxCaller(peer), contract.SessionRefreshRequest{Session: sid}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("sibling: %v", problem)
	}
	for _, caller := range []contract.Principal{boxCaller(peer), {Kind: contract.PrincipalEngine}} {
		if _, problem := s.SessionRefresh(ctx, caller, single); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("caller %+v: %v", caller, problem)
		}
	}
	if s.Stale(child) {
		t.Fatal("a refused caller marked the box")
	}
	for caller, req := range map[string]contract.SessionRefreshRequest{"neither": {}, "both": {Session: child, Room: room}} {
		if _, problem := s.SessionRefresh(ctx, refreshOwner, req); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("%s: %v", caller, problem)
		}
	}
	if _, problem := s.SessionRefresh(ctx, boxCaller(sid), contract.SessionRefreshRequest{Room: other.Room}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("other room: %v", problem)
	}
	if _, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Room: "room_none"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown room: %v", problem)
	}
	// In a room, a session reaches only what is below it, and not itself.
	connectBox(t, s, ctx, sid, newBinary)
	connectBox(t, s, ctx, child, newBinary)
	connectBox(t, s, ctx, peer, newBinary)
	response, problem := s.SessionRefresh(ctx, boxCaller(child), contract.SessionRefreshRequest{Room: room})
	if problem != nil || len(response.Marked) != 0 {
		t.Fatalf("leaf: marked=%v: %v", response.Marked, problem)
	}
	response, problem = s.SessionRefresh(ctx, boxCaller(sid), contract.SessionRefreshRequest{Room: room})
	if problem != nil || !slices.Equal(response.Marked, []contract.SessionID{child}) {
		t.Fatalf("lead: marked=%v: %v", response.Marked, problem)
	}
	if !s.Stale(child) || s.Stale(sid) || s.Stale(peer) {
		t.Fatalf("stale: child=%v lead=%v peer=%v", s.Stale(child), s.Stale(sid), s.Stale(peer))
	}
	if _, problem := s.SessionRefresh(ctx, boxCaller(sid), single); problem != nil {
		t.Fatalf("ancestor: %v", problem)
	}
}
