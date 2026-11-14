package engine

import (
	"context"
	"database/sql"
	"reflect"
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

func TestHostListSessionAllowlistDropsOtherRoomsMetadata(t *testing.T) {
	s, room, root, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "private-room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "shared", State: box.HostReady, Rooms: []contract.RoomID{room, other.Room}}); err != nil {
		t.Fatal(err)
	}
	ownProblem := contract.RunnerImageProblem{Room: room, Image: "own-image", Reason: "own failure"}
	privateProblem := contract.RunnerImageProblem{Room: other.Room, Image: "private-image", Reason: "private build log"}
	s.SetRunnerImageProblems(func() map[contract.HostName][]contract.RunnerImageProblem {
		return map[contract.HostName][]contract.RunnerImageProblem{"shared": {ownProblem, privateProblem}}
	})
	load := func(_ context.Context, hosts []contract.HostSummary) error {
		for i := range hosts {
			if hosts[i].Name == "shared" {
				h := &hosts[i]
				h.CPUFree, h.MemFree, h.DiskFree = 7, 8, 9
				h.Health = contract.HostHealthy
				h.Boxes, h.Runners, h.Leftovers = 12, 13, 14
				h.History = []contract.HostLoadPoint{{CPUUsed: 5}}
				h.GPUs = []contract.GPU{{Index: 1, Name: "GPU", MemBytes: 20, FreeBytes: 10, RequestedBytes: 8}}
			}
		}
		return nil
	}
	for _, live := range []bool{false, true} {
		for _, who := range []contract.Principal{{Kind: contract.PrincipalSession, Session: root}, owner} {
			var got contract.HostListResponse
			if live {
				got, problem = s.HostListWithLoad(ctx, who, contract.HostListRequest{}, load)
			} else {
				got, problem = s.HostList(ctx, who, contract.HostListRequest{})
			}
			if problem != nil {
				t.Fatal(problem)
			}
			var shared contract.HostSummary
			for _, host := range got.Hosts {
				if host.Name == "shared" {
					shared = host
				}
			}
			if who.Kind == contract.PrincipalOwner {
				if len(shared.Rooms) != 2 || len(shared.RunnerImages) != 2 || shared.RunnerImages[1] != privateProblem {
					t.Fatalf("owner metadata lost: %+v", shared)
				}
				continue
			}
			want := contract.HostSummary{Name: "shared", State: "ready", Agents: true, RunnerImages: []contract.RunnerImageProblem{ownProblem}}
			if live {
				want.CPUFree, want.MemFree, want.DiskFree = 7, 8, 9
				want.Health = contract.HostHealthy
				want.GPUs = []contract.GPU{{Index: 1, Name: "GPU", MemBytes: 20, FreeBytes: 10}}
			}
			if !reflect.DeepEqual(shared, want) {
				t.Fatalf("live=%v: session entry %+v, want allowlist %+v", live, shared, want)
			}
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
