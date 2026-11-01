package engine

import (
	"github.com/resident-labs/dyna/box"
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
	if err != nil || len(g) != 1 || !g[0].Expires.IsZero() {
		t.Fatalf("grant %+v %v", g, err)
	}
}

func TestGrantCard(t *testing.T) {
	s, room, worker, ctx := setup(t)
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if err := s.store.SetEngineHost(ctx, "host", false); err != nil {
		t.Fatal(err)
	}
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}, to)
	if card.Title != "asks for host access" || card.Target != (contract.Target{Kind: contract.TargetHost, Name: "host"}) || card.Body != "Run any command on host until the session closes.\nReason: Inspect logs" {
		t.Fatalf("card: %+v", card)
	}
	// The owner granting another session names it, and two hosts keep the room as the target.
	card = s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"a", "b"}, Reason: "Inspect logs"}, contract.Principal{Kind: contract.PrincipalOwner})
	if card.Target.Kind != contract.TargetRoom || !strings.HasPrefix(card.Body, "Let ") || !strings.Contains(card.Body, " run any command on a, b until the session closes.") {
		t.Fatalf("card: %+v", card)
	}

}

func TestHostGrantAfterEngineMove(t *testing.T) {
	s, room, worker, ctx := setup(t)
	no := false
	if err := s.store.AddHost(ctx, box.Host{Name: "capacity", EngineHost: &no}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	args := contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"capacity"}, Reason: "Inspect"}
	result, problem := s.OpSubmit(ctx, to, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: args}})
	if problem != nil || result.Operation.State != contract.OpPending {
		t.Fatalf("submit: %+v %v", result, problem)
	}
	grants, err := s.store.ListActiveGrants(ctx, room, to, s.now())
	if err != nil || len(grants) != 0 {
		t.Fatalf("unapproved grant: %+v %v", grants, err)
	}
	// Startup clears comparisons and expires cards made before the engine moved.
	if err := s.store.ResetEngineHosts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireHostexecGrantRequests(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := s.store.Operation(ctx, result.Operation.ID)
	if err != nil || old.Operation.State != contract.OpExpired {
		t.Fatalf("old request: %+v %v", old, err)
	}
	card := s.HostexecGrant().Card(ctx, args, to)
	if !strings.Contains(card.Body, "may hold the model sign-ins. Approve only if you trust this session with them.") {
		t.Fatal(card.Body)
	}
}

func TestGrantCardEngineHost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	yes := true
	if err := s.store.AddHost(ctx, box.Host{Name: "engine", EngineHost: &yes}); err != nil {
		t.Fatal(err)
	}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"engine"}, Reason: "Inspect"}, to)
	if !strings.Contains(card.Body, "This host also runs the engine and holds the model sign-ins.") {
		t.Fatal(card.Body)
	}
}
