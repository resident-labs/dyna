package engine

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestInstructionsStaleSave(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
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

func TestInstructionsSaveDoesNotWaitForPublisher(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetInstructionsPublisher(func(context.Context) { go func() { close(entered); <-release }() })
	defer close(release)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	before, problem := s.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = s.InstructionsSet(ctx, owner, contract.InstructionsSetRequest{Room: room, Base: before.Hash, Text: "saved"})
	if problem != nil {
		t.Fatal(problem)
	}
	<-entered
	after, problem := s.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{Room: room})
	if problem != nil || after.Text != "saved" {
		t.Fatalf("get: %+v %v", after, problem)
	}
}
