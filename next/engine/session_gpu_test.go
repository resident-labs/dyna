package engine

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestSessionStartWithAGPUNeedsACardBigEnough(t *testing.T) {
	s, room, root, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	start := func(name string, gpu contract.GPUGiB) (contract.SessionStartResponse, *contract.Error) {
		return s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: root, Name: name, Profile: "test", Goal: "g", GPU: gpu})
	}
	if _, problem := start("plain", 0); problem != nil {
		t.Fatalf("no GPU asked: %+v", problem)
	}
	if _, problem := start("none", 1); problem == nil || problem.Code != contract.ErrNoGPU {
		t.Fatalf("no host has a card: %+v", problem)
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "cardless", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if _, problem := start("still-none", 1); problem == nil || problem.Code != contract.ErrNoGPU {
		t.Fatalf("a host without a card: %+v", problem)
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "gpu", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetHostGPUs(ctx, "gpu", []contract.GPU{{Index: 0, Name: "GTX 1050 Ti", MemBytes: 20 * box.GiB}}); err != nil {
		t.Fatal(err)
	}
	if _, problem := start("too-big", 19); problem == nil || problem.Code != contract.ErrGPUTooLarge {
		t.Fatalf("19 GiB on a 20 GiB card with a tenth in reserve: %+v", problem)
	}
	fits, problem := start("fits", 18)
	if problem != nil {
		t.Fatalf("18 GiB: %+v", problem)
	}
	got, err := s.store.Box(ctx, fits.Box)
	if err != nil || got.GPU != 18 {
		t.Fatalf("the box asks %d GiB: %+v, %v", got.GPU, got, err)
	}
}
