package engine

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestRepoCIHostValidation(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, hosts := range [][]string{{"missing"}, {"host", "host"}, {""}} {
		_, problem := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "owner/repo", On: true, Hosts: hosts})
		if problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("accepted %v: %v", hosts, problem)
		}
	}
	if _, problem := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "owner/repo", On: true, Hosts: []string{"host"}}); problem != nil {
		t.Fatal(problem)
	}
	// Omitting the list must preserve it, including across off/on.
	for _, on := range []bool{false, true} {
		if _, problem := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "owner/repo", On: on}); problem != nil {
			t.Fatal(problem)
		}
	}
	rows, err := s.store.CIRepos(ctx)
	if err != nil || len(rows) != 1 || len(rows[0].Only) != 1 || rows[0].Only[0] != "host" {
		t.Fatalf("lost override: %+v %v", rows, err)
	}
	if _, problem := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "owner/repo", On: true, Hosts: []string{}}); problem != nil {
		t.Fatal(problem)
	}
	rows, err = s.store.CIRepos(ctx)
	if err != nil || len(rows[0].Hosts) != 0 {
		t.Fatalf("did not clear: %+v %v", rows, err)
	}
}
