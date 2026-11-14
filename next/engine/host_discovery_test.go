package engine

import (
	"database/sql"
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

func TestHostListServicesStayInSessionRoom(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "shared")
	other, problem := r.s.RoomCreate(r.ctx, r.owner, contract.RoomCreateRequest{Name: "private", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	for i, room := range []contract.RoomID{r.room, other.Room} {
		name, port := contract.ServiceName("own-api"), contract.ServicePort("8443")
		if i == 1 {
			name, port = "private-api", "8444"
		}
		_, problem := r.s.ServiceSet(r.ctx, r.owner, contract.ServiceSetRequest{Room: room, Spec: contract.ServiceSpec{Name: name, Image: svcDigest, Host: "shared", Ports: []contract.ServicePort{port}}})
		if problem != nil {
			t.Fatal(problem)
		}
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='shared'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []contract.Principal{{Kind: contract.PrincipalSession, Session: r.root}, r.owner} {
		got, problem := r.s.HostList(r.ctx, who, contract.HostListRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		var services []contract.HostService
		for _, host := range got.Hosts {
			if host.Name == "shared" {
				services = host.Services
			}
		}
		if who.Kind == contract.PrincipalSession {
			if len(services) != 1 || services[0].Name != "own-api" || services[0].Room != r.room {
				t.Fatalf("session sees other rooms' services: %+v", services)
			}
		} else if len(services) != 2 {
			t.Fatalf("owner lost services: %+v", services)
		}
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
