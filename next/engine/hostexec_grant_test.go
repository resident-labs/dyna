package engine

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"strings"
	"testing"
	"time"
)

func TestRootDefaultGrant(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	g, err := s.store.ListActiveGrants(ctx, room, contract.Principal{Kind: contract.PrincipalSession, Session: root}, s.now())
	if err != nil || len(g) != 1 || g[0].Hosts[0] != contract.AllHosts || !g[0].Expires.IsZero() {
		t.Fatalf("root grant %+v: %v", g, err)
	}
}
func TestGrantOperation(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	target := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	a := contract.HostexecGrantArgs{Room: room, To: target, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}
	res, problem := s.OpSubmit(ctx, owner, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: a}})
	if problem != nil || res.Operation.Class != contract.ClassOwner || res.Operation.State != contract.OpDone {
		t.Fatalf("result %+v %v", res, problem)
	}
	g, err := s.store.ListActiveGrants(ctx, room, target, s.now())
	if err != nil || len(g) != 1 || !g[0].Expires.Equal(s.now().Add(24*time.Hour)) {
		t.Fatalf("grant %+v %v", g, err)
	}
}

func TestGrantCard(t *testing.T) {
	s, room, worker, ctx := setup(t)
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}, contract.Principal{})
	if card.Title != "Grant host access" || !strings.Contains(card.Body, "run any command on host, for 24 hours.\nReason: Inspect logs") {
		t.Fatalf("card: %+v", card)
	}
}
