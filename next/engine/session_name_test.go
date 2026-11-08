package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestStartingASecondOpenSessionWithANameSaysSo(t *testing.T) {
	s, room, root, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	first, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: root, Name: "review", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: root, Name: "review", Profile: "test"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "session review ("+string(first.Session)+") is already open in this room") {
		t.Fatalf("%+v", e)
	}
}
