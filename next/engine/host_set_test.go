package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// host.set is the owner's, needs a switch, and host.list shows both.
func TestHostSetTurnsSwitchesForTheOwner(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	off, on := false, true
	if _, problem := s.HostSet(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.HostSetRequest{Host: "host", Agents: &off}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session: %v", problem)
	}
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("no switch: %v", problem)
	}
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "missing", CI: &on}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown host: %v", problem)
	}
	switches := func() (bool, bool) {
		t.Helper()
		list, problem := s.HostList(ctx, owner, contract.HostListRequest{})
		if problem != nil || len(list.Hosts) != 2 {
			t.Fatalf("hosts: %+v %v", list, problem)
		}
		return list.Hosts[1].Agents, list.Hosts[1].CI
	}
	if agents, ci := switches(); !agents || ci {
		t.Fatalf("as added: agents %v ci %v", agents, ci)
	}
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", CI: &on}); problem != nil {
		t.Fatal(problem)
	}
	if agents, ci := switches(); !agents || !ci {
		t.Fatalf("ci on: agents %v ci %v", agents, ci)
	}
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", Agents: &off}); problem != nil {
		t.Fatal(problem)
	}
	if agents, ci := switches(); agents || !ci {
		t.Fatalf("agents off: agents %v ci %v", agents, ci)
	}
}

// With agents off, a new session's box waits with room-host while the box already there keeps running; agents on places it.
func TestAgentsOffKeepsNewBoxesOffTheHost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	off, on := false, true
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", Agents: &off}); problem != nil {
		t.Fatal(problem)
	}
	started, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "late", Profile: "test", Goal: "goal", Brief: "brief"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	read := func(id contract.SessionID) (contract.BoxState, string, contract.HostName) {
		t.Helper()
		session, err := s.store.Session(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.store.Box(ctx, session.Box)
		if err != nil {
			t.Fatal(err)
		}
		return b.State, b.Reason, b.Host
	}
	if state, reason, host := read(started.Session); state != contract.BoxPending || reason != "room-host" || host != "" {
		t.Fatalf("new box: %s %q on %q", state, reason, host)
	}
	if state, _, host := read(worker); state == contract.BoxPending || host != "host" {
		t.Fatalf("the box already there: %s on %q", state, host)
	}
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", Agents: &on}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if state, _, host := read(started.Session); state == contract.BoxPending || host != "host" {
		t.Fatalf("agents back on: %s on %q", state, host)
	}
}
