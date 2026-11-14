package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestRoomHostsRequiresExplicitChange(t *testing.T) {
	r := setupPool(t)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: r.root}
	for _, req := range []contract.RoomHostsRequest{
		{Room: r.room},
		{Room: r.room, Only: []string{"host"}, Clear: true},
	} {
		got, problem := r.s.RoomHosts(r.ctx, who, req)
		if problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "host list") || got.Operation != "" {
			t.Fatalf("got %+v, problem %v", got, problem)
		}
	}
	ops, problem := r.s.OpList(r.ctx, r.owner, contract.OpListRequest{Room: r.room})
	if problem != nil || len(ops.Operations) != 0 {
		t.Fatalf("operations %+v: %v", ops, problem)
	}
	got, problem := r.s.RoomHosts(r.ctx, who, contract.RoomHostsRequest{Room: r.room, Clear: true})
	if problem != nil || got.Operation == "" {
		t.Fatalf("explicit clear %+v: %v", got, problem)
	}
}

func TestHostListKeepsRoomScopeAndUnavailableHosts(t *testing.T) {
	s, room, root, ctx := setup(t)
	other, problem := s.RoomCreate(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomCreateRequest{Name: "elsewhere", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, h := range []box.Host{
		{Name: "off", State: box.HostReady, NoAgents: true},
		{Name: "ready", State: box.HostReady},
		{Name: "other", State: box.HostReady, Rooms: []contract.RoomID{other.Room}},
		{Name: "unselected", State: box.HostReady},
	} {
		if err := s.store.AddHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.SetRoomOnly(ctx, room, []string{"off", "ready", "other"}); err != nil {
		t.Fatal(err)
	}
	got, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostListRequest{})
	if problem != nil || len(got.Hosts) != 2 {
		t.Fatalf("hosts %+v: %v", got, problem)
	}
	if got.Hosts[0].Name != "off" || got.Hosts[0].State != "agents off" || got.Hosts[1].Name != "ready" || got.Hosts[1].State != "ready" {
		t.Fatalf("hosts %+v", got)
	}
}
