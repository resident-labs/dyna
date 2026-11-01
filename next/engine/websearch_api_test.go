package engine

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestSearchKeysAreOwnerOnly(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	req := contract.WebsearchSetKeyRequest{Provider: "muse", Key: "private-key"}
	if _, problem := s.WebsearchSetKey(ctx, session, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session set: %v", problem)
	}
	if _, problem := s.WebsearchSetKey(ctx, owner, req); problem != nil {
		t.Fatal(problem)
	}
	got, err := s.store.SearchKey(ctx, "muse")
	if err != nil || got != req.Key {
		t.Fatalf("saved key: %v", err)
	}
	if _, problem := s.WebsearchRemoveKey(ctx, session, contract.WebsearchRemoveKeyRequest{Provider: "muse"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session remove: %v", problem)
	}
	if _, problem := s.WebsearchRemoveKey(ctx, owner, contract.WebsearchRemoveKeyRequest{Provider: "muse"}); problem != nil {
		t.Fatal(problem)
	}
	if _, err := s.store.SearchKey(ctx, "muse"); err == nil {
		t.Fatal("key remains")
	}
}
