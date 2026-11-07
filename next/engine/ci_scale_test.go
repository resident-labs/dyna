package engine

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

// The same persisted snapshot feeds host.list and the runner's admission path.
func TestCIScalePersistsIntoAdmission(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hosts[0].CIScale != 1 {
		t.Fatalf("default: %+v", hosts)
	}
	for _, n := range []int{0, 5, -1} {
		if _, p := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", CIScale: &n}); p == nil || p.Code != contract.ErrInvalid {
			t.Fatalf("range %d: %v", n, p)
		}
	}
	two := 2
	if _, p := s.HostSet(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.HostSetRequest{Host: "host", CIScale: &two}); p == nil || p.Code != contract.ErrForbidden {
		t.Fatal("session changed scale")
	}
	for i := 0; i < 2; i++ {
		if _, p := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "host", CIScale: &two}); p != nil {
			t.Fatal(p)
		}
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"host.switches_changed"})
	if err != nil || len(events) != 1 {
		t.Fatalf("audit: %+v %v", events, err)
	}
	body := events[0].Body.(contract.HostSwitchesChanged)
	if body.CIScale == nil || *body.CIScale != 2 {
		t.Fatalf("audit body: %+v", body)
	}
	list, p := s.HostList(ctx, owner, contract.HostListRequest{})
	if p != nil {
		t.Fatal(p)
	}
	if list.Hosts[1].CIScale != 2 {
		t.Fatalf("list: %+v", list)
	}
	hosts, err = s.reconciler.AdmissionHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := hosts[0]
	if h.CIScale != 2 {
		t.Fatalf("admission snapshot: %+v", h)
	}
	h.State = box.HostReady
	h.CPUs = 64
	h.MemBytes = 32 * box.GiB
	h.DiskBytes = 10000 * box.GiB
	size, _ := box.Size(contract.BoxSizeLarge)
	request := box.Box{ID: "new", Room: room, Image: box.RunnerImage, Size: size}
	var running []box.Box
	for _, id := range []contract.BoxID{"one", "two", "three", "four"} {
		if got := box.Admit([]box.Host{h}, running, request); got.Host != h.Name {
			t.Fatalf("stored scale lost: %+v", got)
		}
		b := request
		b.ID = id
		b.Host = h.Name
		b.State = contract.BoxReady
		running = append(running, b)
	}
	if got := box.Admit([]box.Host{h}, running, request); got.Reason != box.PendingMemory {
		t.Fatalf("limit: %+v", got)
	}
}
