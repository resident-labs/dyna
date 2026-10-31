package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestInstructionsStaleSave(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetFilesDir(t.TempDir())
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	before, problem := s.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: room})
	if problem != nil || before.Text != "" || before.Hash == "" || before.SavedAt != nil {
		t.Fatalf("empty instructions: %+v, %v", before, problem)
	}
	saved, problem := s.InstructionsSet(ctx, owner, contract.InstructionsSetRequest{Room: room, Base: before.Hash, Text: "First"})
	if problem != nil || saved.Hash == before.Hash {
		t.Fatalf("save: %+v, %v", saved, problem)
	}
	_, problem = s.InstructionsSet(ctx, owner, contract.InstructionsSetRequest{Room: room, Base: before.Hash, Text: "Stale"})
	if problem == nil || problem.Code != contract.ErrStale {
		t.Fatalf("stale save: %v", problem)
	}
	after, problem := s.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: room})
	if problem != nil || after.Text != "First" || after.Hash != saved.Hash || after.SavedAt == nil {
		t.Fatalf("read after stale save: %+v, %v", after, problem)
	}
	_, problem = s.InstructionsGet(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.InstructionsGetRequest{Room: room})
	if problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session read: %v", problem)
	}
}
