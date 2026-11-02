package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

func TestOperationReplayRejectsDifferentDeviceScope(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "capacity"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	admin := contract.Principal{Kind: contract.PrincipalOwner, Device: "admin-device"}
	limited := contract.Principal{Kind: contract.PrincipalOwner, Device: "operate-device"}
	target := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	req := contract.OpSubmitRequest{ClientID: "shared", Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: contract.HostexecGrantArgs{Room: room, To: target, Hosts: []contract.HostName{"capacity"}, Reason: "Inspect"}}}
	if _, problem := s.OpSubmit(ctx, admin, req); problem != nil {
		t.Fatal(problem)
	}
	req.Operation.Kind = "pr.open"
	req.Operation.Args = contract.PrOpenArgs{Title: "test"}
	if _, problem := s.OpSubmit(ctx, limited, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("different kind exposed operation: %v", problem)
	}
	req.Operation.Kind = "hostexec.grant"
	req.Operation.Room = "another-room"
	if _, problem := s.OpSubmit(ctx, limited, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("different room exposed operation: %v", problem)
	}
}
