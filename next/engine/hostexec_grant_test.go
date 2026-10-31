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
	s, room, worker, ctx := setup(t)
	entry := s.HostexecGrant()
	approve := entry.Card(ctx, contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Mode: contract.GrantModeApprove}, contract.Principal{})
	auto := entry.Card(ctx, contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Mode: contract.GrantModePreapproved}, contract.Principal{})
	if !strings.Contains(approve.Body, "asking you before each run") || !strings.Contains(auto.Body, "without asking each time") {
		t.Fatalf("cards: %q / %q", approve.Body, auto.Body)
	}
}

func TestGrantCardDistinguishesArguments(t *testing.T) {
	s, room, worker, ctx := setup(t)
	a := contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Commands: []contract.CommandRule{{Program: "sh", Args: []string{"-c", "a; b"}}}}
	one := s.HostexecGrant().Card(ctx, a, contract.Principal{}).Body
	a.Commands[0].Args = []string{"-c", "a", "b"}
	two := s.HostexecGrant().Card(ctx, a, contract.Principal{}).Body
	if one == two || !strings.Contains(one, `"a; b"`) {
		t.Fatalf("ambiguous cards: %q / %q", one, two)
	}
}

func TestGrantCardEscapesControlCharactersAndNamesWildcard(t *testing.T) {
	s, room, worker, ctx := setup(t)
	a := contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Commands: []contract.CommandRule{{Program: "sh", Args: []string{"a\nb"}}}}
	body := s.HostexecGrant().Card(ctx, a, contract.Principal{}).Body
	if !strings.Contains(body, `"a\nb"`) || strings.Contains(body, "a\nb") {
		t.Fatalf("control character: %q", body)
	}
	a.Commands = []contract.CommandRule{{Program: "*", Prefix: true}}
	body = s.HostexecGrant().Card(ctx, a, contract.Principal{}).Body
	if !strings.Contains(body, "any command") {
		t.Fatalf("wildcard: %q", body)
	}
}

func TestGrantCardEscapesHost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	a := contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Hosts: []contract.HostName{"hermeslab\nany command"}, Commands: []contract.CommandRule{{Program: "git"}}}
	body := s.HostexecGrant().Card(ctx, a, contract.Principal{}).Body
	before, _, found := strings.Cut(body, ":\n")
	if !found || !strings.Contains(before, `"hermeslab\nany command"`) || strings.Contains(before, "\n") {
		t.Fatalf("host: %q", body)
	}
}
