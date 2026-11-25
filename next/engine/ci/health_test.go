package ci

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

// A degraded host still owns its live runners, slots and repository cap; only new admission stops.
func TestDegradedStillCountsRunningRunners(t *testing.T) {
	f := newFixture(1)
	defer f.stop()
	if capacity := f.scale(1); capacity != 1 {
		t.Fatalf("initial capacity %d", capacity)
	}
	f.addHost(readyHost("second", 64*box.GiB))
	f.pool.Health = func(name contract.HostName) contract.HostHealth {
		if name == "pve" {
			return contract.HostDegraded
		}
		return contract.HostHealthy
	}
	f.scale(1)
	count := len(f.drivers["pve"].runners) + len(f.drivers["second"].runners)
	if count != 1 {
		t.Fatalf("max=1 and assigned=1, but %d runners run", count)
	}
}

func TestDegradedSchedulingHealthStopsNewRunners(t *testing.T) {
	f := newFixture(2)
	f.pool.Health = func(contract.HostName) contract.HostHealth { return contract.HostDegraded }
	if capacity := f.scale(1); capacity != 0 || len(f.sets.names) != 0 {
		t.Fatalf("degraded capacity %d: %v", capacity, f.sets.names)
	}
	if f.drivers["pve"].listCalls != 0 {
		t.Fatalf("degraded host with no runners was surveyed %d times", f.drivers["pve"].listCalls)
	}
	f.pool.Health = func(contract.HostName) contract.HostHealth { return contract.HostHealthy }
	if capacity := f.scale(1); capacity == 0 {
		t.Fatal("good probe did not restore runner admission")
	}
	f.stop()
}

// A degraded host with no known runners is skipped during scale, while Tick still surveys it (bounded by SurveyLimit) so
// stopped runners and proxies left behind after a restart are swept.
func TestDegradedHostSkippedInScaleButSweptInTick(t *testing.T) {
	f := newFixture(2)
	defer f.stop()
	size, _ := box.Size(large)
	desktop := f.addHost(readyHost("desktop", 64*box.GiB))
	desktop.runners = []box.Runner{{Name: "dyna-ci-desktop-dead", Repo: repo, ID: 9, Slot: 0, Size: size, Running: false}}
	desktop.egress = EgressStale
	f.pool.Health = func(name contract.HostName) contract.HostHealth {
		if name == "desktop" {
			return contract.HostDegraded
		}
		return contract.HostHealthy
	}
	// scale skips desktop (empty p.placed) and places on pve.
	if capacity := f.scale(1); capacity < 1 || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("capacity=%d, pve runners=%d", capacity, len(f.drivers["pve"].runners))
	}
	if desktop.listCalls != 0 {
		t.Fatalf("degraded host was surveyed %d times during scale", desktop.listCalls)
	}
	// Tick still surveys desktop and sweeps its stopped runner and stale proxy.
	f.tick(t)
	if desktop.listCalls != 1 || len(desktop.runners) != 0 || desktop.egress != EgressAbsent {
		t.Fatalf("Tick did not sweep degraded host: listCalls=%d runners=%+v egress=%v", desktop.listCalls, desktop.runners, desktop.egress)
	}
}
