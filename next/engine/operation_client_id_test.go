package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

func TestOperationClientID(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "capacity"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	args := contract.HostexecGrantArgs{Room: room, To: caller, Hosts: []contract.HostName{"capacity"}, Reason: "Inspect"}
	req := contract.OpSubmitRequest{ClientID: strings.Repeat("x", 64), Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: args}}
	var results [2]contract.OpSubmitResponse
	var problems [2]*contract.Error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], problems[i] = s.OpSubmit(ctx, caller, req) }(i)
	}
	wg.Wait()
	if problems[0] != nil || problems[1] != nil || results[0].Operation.ID != results[1].Operation.ID {
		t.Fatalf("concurrent retries: %+v %+v", results, problems)
	}
	if _, problem := s.OpWithdraw(ctx, caller, contract.OpWithdrawRequest{ID: results[0].Operation.ID}); problem != nil {
		t.Fatal(problem)
	}
	req.Operation.Args = nil
	got, problem := s.OpSubmit(ctx, caller, req)
	if problem != nil || got.Operation.ID != results[0].Operation.ID || got.Operation.State != contract.OpWithdrawn {
		t.Fatalf("retry: %+v %v", got, problem)
	}
	req.ClientID += "x"
	if _, problem := s.OpSubmit(ctx, caller, req); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("long key: %v", problem)
	}
}
