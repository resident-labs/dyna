package engine

import (
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionStartClientID(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.SessionStartRequest{Room: room, Name: "replay", Profile: "test", ClientID: strings.Repeat("x", 64)}
	var results [2]contract.SessionStartResponse
	var problems [2]*contract.Error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], problems[i] = s.SessionStart(ctx, owner, req) }(i)
	}
	wg.Wait()
	if problems[0] != nil || problems[1] != nil || results[0] != results[1] {
		t.Fatalf("concurrent retries: %+v %+v", results, problems)
	}
	req.Name = "changed"
	if got, problem := s.SessionStart(ctx, owner, req); problem != nil || got != results[0] {
		t.Fatalf("retry: %+v %v", got, problem)
	}
	req.Name = "another"
	if got, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, req); problem != nil || got == results[0] {
		t.Fatalf("other caller: %+v %v", got, problem)
	}
	req.ClientID += "x"
	if _, problem := s.SessionStart(ctx, owner, req); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("long key: %v", problem)
	}
}
