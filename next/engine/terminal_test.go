package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestHostListOwner(t *testing.T) {
	s, _, sid, ctx := setup(t)
	list, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(list.Hosts) != 2 || list.Hosts[0] != contract.ControlPlane || list.Hosts[1] != "host" {
		t.Fatalf("hosts: %+v %v", list, problem)
	}
	_, problem = s.HostList(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.HostListRequest{})
	if problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
}

func TestTerminalKeepsBoxAwake(t *testing.T) {
	s, host, _, sid, ctx := quietSession(t, 0)
	_, release, problem := s.Terminal(ctx, sid, "")
	if problem != nil {
		t.Fatal(problem)
	}
	s.ReconcileDormancy(ctx, false, true)
	if host.stops != 0 {
		t.Fatal("terminal box stopped")
	}
	release()
	s.ReconcileDormancy(ctx, false, true)
	if host.stops != 1 {
		t.Fatal("box did not stop after terminal closed")
	}
	_, _, problem = s.Terminal(ctx, sid, "")
	if problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("stopped box: %v", problem)
	}
}
