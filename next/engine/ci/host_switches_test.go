package ci

import (
	"github.com/resident-labs/dyna/box"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// A host with ci off gets no runner, and its runners come back when ci is on again.
func TestThePoolSkipsACIOffHost(t *testing.T) {
	f := newFixture(2)
	f.store.hosts[0].CI = false
	if capacity := f.scale(1); capacity != 0 || len(f.sets.names) != 0 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("ci off: capacity %d, registered %v, runners %+v", capacity, f.sets.names, f.drivers["pve"].runners)
	}
	f.store.hosts[0].CI = true
	if capacity := f.scale(1); capacity != 2 || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("ci back on: capacity %d, runners %+v", capacity, f.drivers["pve"].runners)
	}
}

// Turning ci off keeps a working runner: it finishes its job, the pool counts it, and nothing new starts beside it.
func TestCIOffLetsRunningJobsFinish(t *testing.T) {
	f := newFixture(3)
	defer f.stop()
	f.scale(1)
	d := f.drivers["pve"]
	f.store.hosts[0].CI = false
	if capacity := f.scale(3); capacity != 1 || len(d.runners) != 1 || len(f.sets.names) != 1 {
		t.Fatalf("capacity %d, runners %+v, registered %v", capacity, d.runners, f.sets.names)
	}
	f.tick(t)
	if len(d.runners) != 1 || len(d.removed) != 0 || len(f.api.deleted) != 0 || !d.egress {
		t.Fatalf("a tick removed the working runner or its proxy: runners %+v, removed %v, deleted %v", d.runners, d.removed, f.api.deleted)
	}
	d.runners = nil // its job ended
	f.tick(t)
	if d.egress {
		t.Fatal("a host with ci off kept its proxy after its last runner")
	}
}

// A CI host keeps sessions off with agents off, and its room list is for sessions too: the pool still starts its runners.
// With both switches off it starts none.
func TestAgentsOffStillServesCIAndBothOffServesNothing(t *testing.T) {
	f := newFixture(1)
	f.store.hosts[0].NoAgents = true
	f.store.hosts[0].Rooms = []contract.RoomID{"elsewhere"}
	if capacity := f.scale(1); capacity != 1 || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("agents off, ci on: capacity %d, runners %+v", capacity, f.drivers["pve"].runners)
	}
	g := newFixture(1)
	g.store.hosts[0].NoAgents, g.store.hosts[0].CI = true, false
	if capacity := g.scale(1); capacity != 0 || len(g.drivers["pve"].runners) != 0 {
		t.Fatalf("both off: capacity %d, runners %+v", capacity, g.drivers["pve"].runners)
	}
}

// A session box waiting for CPU or memory comes before a runner: no runner starts on a host that box could take, so a freed
// slot goes to the box. A box waiting for anything else holds nobody back.
func TestARunnerSkipsAHostWhereASessionBoxWaitsForRoom(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	for _, tc := range []struct {
		name    string
		pending box.Box
		wantPVE int
	}{
		{"cpu, not yet placed", box.Box{ID: "b", Room: "r2", Size: size, State: contract.BoxPending, Reason: string(box.PendingCPU)}, 0},
		{"memory, on its host", box.Box{ID: "b", Room: "r2", Host: "pve", Image: "img", Size: size, State: contract.BoxPending, Reason: string(box.PendingMemory)}, 0},
		{"disk", box.Box{ID: "b", Room: "r2", Size: size, State: contract.BoxPending, Reason: string(box.PendingDisk)}, 1},
	} {
		f := newFixture(0)
		arch := f.addHost(readyHost("arch", 32*box.GiB))        // pve has more free memory, so it wins unless it is skipped
		f.store.hosts[1].Rooms = []contract.RoomID{"elsewhere"} // the waiting box cannot go to arch
		f.store.boxes = []box.Box{tc.pending}
		f.scale(1)
		if got := len(f.drivers["pve"].runners); got != tc.wantPVE || len(arch.runners) != 1-tc.wantPVE {
			t.Errorf("%s: pve %d, arch %d", tc.name, got, len(arch.runners))
		}
	}
}
