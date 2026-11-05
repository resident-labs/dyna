package ci

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
)

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
