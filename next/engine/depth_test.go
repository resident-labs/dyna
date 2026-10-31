package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionDepthLimit(t *testing.T) {
	for _, limit := range []int{3, 0} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			s, room, root, ctx := setupRoot(t)
			s.SetMaxDepth(limit)
			owner := contract.Principal{Kind: contract.PrincipalOwner}
			start := func(parent contract.SessionID, name string) (contract.SessionStartResponse, *contract.Error) {
				return s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: name, Profile: "test"})
			}
			a, e := start(root, "a")
			if e != nil {
				t.Fatal(e)
			}
			b, e := start(a.Session, "b")
			if e != nil {
				t.Fatal(e)
			}
			_, e = start(b.Session, "c")
			if limit == 0 && e != nil {
				t.Fatal(e)
			}
			if limit != 0 && (e == nil || !strings.Contains(e.Message, "depth 4") || !strings.Contains(e.Message, "limit 3")) {
				t.Fatalf("unexpected error: %v", e)
			}
		})
	}
}

func TestMoveRejectsDeepSubtree(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetMaxDepth(3)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	start := func(parent contract.SessionID, name string) contract.SessionID {
		r, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: name, Profile: "test"})
		if e != nil {
			t.Fatal(e)
		}
		return r.Session
	}
	a := start(root, "a")
	start(a, "b")
	dest := start(root, "dest")
	_, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: a, NewParent: dest})
	if e == nil || !strings.Contains(e.Message, "depth 4") {
		t.Fatalf("unexpected error: %v", e)
	}
}
