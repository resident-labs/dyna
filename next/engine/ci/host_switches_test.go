package ci

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A host with ci off gets no runner, though a repository still names it, and its runners come back when ci is on again.
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
	if len(d.runners) != 1 || len(d.removed) != 0 || len(f.api.deleted) != 0 {
		t.Fatalf("a tick removed the working runner: runners %+v, removed %v, deleted %v", d.runners, d.removed, f.api.deleted)
	}
}

// With two hosts named, a job goes to the one with ci on, even when the other has more free memory.
func TestRunnersGoToTheHostWithCIOn(t *testing.T) {
	f := newFixture(1)
	f.drivers["big"] = &fakeDriver{}
	big := readyHost("big", 128*box.GiB)
	big.CI = false
	f.store.hosts = append(f.store.hosts, big)
	f.store.settings = append(f.store.settings, setting("big", 1))
	if capacity := f.scale(2); capacity != 1 || len(f.drivers["big"].runners) != 0 || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("capacity %d, big %d, pve %d", capacity, len(f.drivers["big"].runners), len(f.drivers["pve"].runners))
	}
}

// A CI host keeps sessions off with agents off, and the pool still starts its runners. With both switches off it starts none.
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
