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
	f.pool.Health = func(contract.HostName) contract.HostHealth { return contract.HostHealthy }
	if capacity := f.scale(1); capacity == 0 {
		t.Fatal("good probe did not restore runner admission")
	}
	f.stop()
}
