package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"

	"github.com/resident-labs/dyna/contract"
)

type fakeRegistrar struct{ called bool }

func (f *fakeRegistrar) AddHost(context.Context, contract.HostAddRequest) (bool, error) {
	f.called = true
	return false, nil
}

func (*fakeRegistrar) HostLoad(context.Context, []contract.HostSummary) error { return nil }
func (*fakeRegistrar) SeedAgy(context.Context, contract.HostName) error       { return nil }

func TestHostAddRequiresOwnerAndConfirmation(t *testing.T) {
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	_, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host"})
	if err == nil || err.Code != contract.ErrInvalid || f.called {
		t.Fatalf("error=%v called=%v", err, f.called)
	}
}

type listStore struct{ boxStore }

func (listStore) Boxes(context.Context) ([]box.Box, error) {
	return []box.Box{{Host: "host", State: contract.BoxReady}, {Host: "host", State: contract.BoxStopped}}, nil
}
func TestHostListIncludesLoadAndRunningBoxes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AddHost(ctx, box.Host{Name: "host", CPUs: 8, MemBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: listStore{}, loads: map[contract.HostName]hostLoad{"host": {summary: contract.HostSummary{Reachable: true, Health: contract.HostDegraded, CPUUsed: 1.5, MemUsed: 512, DiskBytes: 100, DiskUsed: 20}}}}
	svc := serviceWithHosts{Service: engine.New(st, nil, nil, time.Now), boxes: b}
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	want := contract.HostSummary{Reservations: &contract.HostReservations{CPUCap: 32000, MemoryCap: 16384}, State: "degraded", Health: contract.HostDegraded, CPUFree: 6.5, MemFree: 3584, DiskFree: 80, DiskBytes: 100, DiskUsed: 20, DiskLow: true, Shared: []contract.RoomShared{}, Name: "host", Reachable: true, Boxes: 1, CPUs: 8, MemBytes: 4096, Agents: true, CIScale: 1, CPUUsed: 1.5, MemUsed: 512, History: []contract.HostLoadPoint{}}
	if problem != nil || len(got.Hosts) != 2 || !reflect.DeepEqual(got.Hosts[1], want) {
		t.Fatalf("hosts: %+v, error: %v", got, problem)
	}
}

type missingPodmanRegistrar struct{ fakeRegistrar }

func (*missingPodmanRegistrar) AddHost(context.Context, contract.HostAddRequest) (bool, error) {
	return false, podman.RequiredError{}
}
func TestHostAddRefusesMissingPodman(t *testing.T) {
	s := serviceWithHosts{boxes: &missingPodmanRegistrar{}}
	_, problem := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host", OwnerOnlyAdmin: true})
	if problem == nil || problem.Code != contract.ErrPodmanRequired || !strings.Contains(problem.Message, "Podman 5") {
		t.Fatalf("problem: %+v", problem)
	}
}

func TestHostAddWithoutTheFlagNamesItAndWhoNeedsIt(t *testing.T) {
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	_, problem := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host"})
	want := "--owner-only-admin is required: it confirms that only the owner administers the machine, which a host that runs boxes or CI runners needs; a host added with --agents off --ci off (exec only) does not"
	if problem == nil || problem.Code != contract.ErrInvalid || problem.Message != want || f.called {
		t.Fatalf("problem=%+v called=%v", problem, f.called)
	}
}

func TestHostAddChecksNameAndSSHAtTheBoundary(t *testing.T) {
	off := false
	for _, req := range []contract.HostAddRequest{
		{Name: "bad\nname", SSH: "user@host", OwnerOnlyAdmin: true},
		{Name: "*bold*", SSH: "user@host", OwnerOnlyAdmin: true},
		{Name: "ok", SSH: "user@ho st", OwnerOnlyAdmin: true},
		{Name: "ok", SSH: "-oProxyCommand=x@host", OwnerOnlyAdmin: true},
		{Name: "ok", SSH: "user@host:22", OwnerOnlyAdmin: true},
	} {
		f := &fakeRegistrar{}
		s := serviceWithHosts{boxes: f}
		if _, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, req); err == nil || err.Code != contract.ErrInvalid || f.called {
			t.Fatalf("%+v: error=%v called=%v", req, err, f.called)
		}
	}
	// An exec-only host is added with no administration flag, as the documented command has none.
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	if _, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "omarchy", SSH: "ari@omarchy", Agents: &off, CI: &off}); err != nil || !f.called {
		t.Fatalf("error=%v called=%v", err, f.called)
	}
}

// A seed copies main, whose token a switch replaces: it is refused while a switch is held or another seed, switch or import runs, and
// nothing is copied then.
func TestAgySeedIsRefusedWhileASwitchIsHeldOrAnotherOpRuns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AddHost(ctx, box.Host{Name: "other", CPUs: 8, MemBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	f := &seedCounter{}
	svc := &serviceWithHosts{Service: engine.New(st, nil, nil, time.Now), boxes: f}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := svc.HostAgySeed(ctx, owner, contract.HostAgySeedRequest{Host: "other"}); problem != nil || f.seeded != 1 {
		t.Fatalf("a seed with nothing held: %v %d", problem, f.seeded)
	}
	if _, err := st.BeginAgySwitch(ctx, "agy_x"); err != nil {
		t.Fatal(err)
	}
	if _, problem := svc.HostAgySeed(ctx, owner, contract.HostAgySeedRequest{Host: "other"}); problem == nil || problem.Code != contract.ErrConflict || f.seeded != 1 {
		t.Fatalf("a seed while held: %v %d", problem, f.seeded)
	}
	if err := st.AbortAgySwitch(ctx); err != nil {
		t.Fatal(err)
	}
	end, ok := svc.Service.AgyExclusive()
	if !ok {
		t.Fatal("exclusive refused")
	}
	if _, problem := svc.HostAgySeed(ctx, owner, contract.HostAgySeedRequest{Host: "other"}); problem == nil || problem.Code != contract.ErrConflict || f.seeded != 1 {
		t.Fatalf("a seed while another op runs: %v %d", problem, f.seeded)
	}
	end()
	if _, problem := svc.HostAgySeed(ctx, owner, contract.HostAgySeedRequest{Host: "other"}); problem != nil || f.seeded != 2 {
		t.Fatalf("a seed after both ended: %v %d", problem, f.seeded)
	}
}

type seedCounter struct {
	fakeRegistrar
	seeded int
}

func (s *seedCounter) SeedAgy(context.Context, contract.HostName) error { s.seeded++; return nil }
