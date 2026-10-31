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
	if err != nil || len(g) != 1 || g[0].Mode != contract.GrantModePreapproved || g[0].Hosts[0] != contract.AllHosts || g[0].Commands[0].Program != "*" || !g[0].Commands[0].Prefix || !g[0].Expires.IsZero() {
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
	a := contract.HostexecGrantArgs{Room: room, To: target, Hosts: []contract.HostName{"host"}, Commands: []contract.CommandRule{{Program: "git"}}, Mode: contract.GrantModeApprove}
	res, problem := s.OpSubmit(ctx, owner, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: a}})
	if problem != nil || res.Operation.Class != contract.ClassOwner || res.Operation.State != contract.OpDone {
		t.Fatalf("result %+v %v", res, problem)
	}
	g, err := s.store.ListActiveGrants(ctx, room, target, s.now())
	if err != nil || len(g) != 1 || !g[0].Expires.Equal(s.now().Add(24*time.Hour)) {
		t.Fatalf("grant %+v %v", g, err)
	}
}

func TestGrantCardShowsMode(t *testing.T) {
	s, _, _, _ := setup(t)
	entry := s.HostexecGrant()
	approve := entry.Card(contract.HostexecGrantArgs{Mode: contract.GrantModeApprove}, contract.Principal{})
	auto := entry.Card(contract.HostexecGrantArgs{Mode: contract.GrantModePreapproved}, contract.Principal{})
	if !strings.Contains(approve.Body, "approve (each run asks the owner)") || !strings.Contains(auto.Body, "preapproved (runs without asking)") {
		t.Fatalf("cards: %q / %q", approve.Body, auto.Body)
	}
}
