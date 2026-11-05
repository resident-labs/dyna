package ci

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
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

// A session box waiting for CPU or memory comes before a runner: no runner starts on a host that box would take once its
// runners were gone, so a freed slot goes to the box. A box that fits nowhere, that its rule keeps off the host, that other
// boxes keep out, or that waits for something else, holds nobody back.
func TestARunnerSkipsAHostWhereASessionBoxWaitsForRoom(t *testing.T) {
	small, _ := box.Size(contract.BoxSizeSmall)
	huge := contract.BoxSize{Name: contract.BoxSizeXLarge, CPUs: 1000, MemBytes: small.MemBytes}
	waiting := func(reason box.PendingReason, host contract.HostName, size contract.BoxSize, only ...string) store.LifecycleBox {
		image := ""
		if host != "" {
			image = "img"
		}
		return store.LifecycleBox{Box: box.Box{ID: "b", Room: "r2", Host: host, Image: image, Size: size, State: contract.BoxPending, Reason: string(reason)}, Only: only}
	}
	full := store.LifecycleBox{Box: box.Box{ID: "full", Host: "pve", Image: "img", Size: contract.BoxSize{Name: contract.BoxSizeAgent, CPUs: 240, MemBytes: small.MemBytes}, State: contract.BoxReady}}
	// arch is busier, so a runner prefers pve unless pve is skipped.
	busyArch := store.LifecycleBox{Box: box.Box{ID: "busy", Host: "arch", Image: "img", Size: contract.BoxSize{Name: contract.BoxSizeAgent, CPUs: 248, MemBytes: small.MemBytes}, State: contract.BoxReady}}
	for _, tc := range []struct {
		name  string
		boxes []store.LifecycleBox
		onPVE int // runners on pve; the rest go to arch
	}{
		{"cpu, not yet placed", []store.LifecycleBox{waiting(box.PendingCPU, "", small)}, 0},
		{"memory, on its host", []store.LifecycleBox{waiting(box.PendingMemory, "pve", small)}, 0},
		{"disk", []store.LifecycleBox{waiting(box.PendingDisk, "", small)}, 1},
		{"too big for any host", []store.LifecycleBox{waiting(box.PendingCPU, "", huge)}, 1},
		{"its rule names another host", []store.LifecycleBox{waiting(box.PendingCPU, "", small, "elsewhere")}, 1},
		{"other boxes keep it out", []store.LifecycleBox{full, busyArch, waiting(box.PendingCPU, "", contract.BoxSize{Name: contract.BoxSizeLarge, CPUs: 20, MemBytes: small.MemBytes})}, 1},
	} {
		f := newFixture(0)
		arch := f.addHost(readyHost("arch", 32*box.GiB))        // pve has more free memory, so it wins unless it is skipped
		f.store.hosts[1].Rooms = []contract.RoomID{"elsewhere"} // the waiting box cannot go to arch
		f.store.boxes = tc.boxes
		f.scale(1)
		if got := len(f.drivers["pve"].runners); got != tc.onPVE || len(arch.runners) != 1-tc.onPVE {
			t.Errorf("%s: pve %d, arch %d", tc.name, got, len(arch.runners))
		}
	}
}

// A pending service comes before a runner as a pending box does, on the hosts it could take: its declared host and its room's rule
// narrow them, so a service that cannot go to a host holds no runner back there.
func TestARunnerSkipsAHostWhereAServiceWaitsForRoom(t *testing.T) {
	waiting := func(host contract.HostName, only ...string) store.ServiceRow {
		parts, err := contract.ServiceSpec{Name: "svc", Image: "ghcr.io/o/r@sha256:" + strings.Repeat("a", 64), CPUs: 1, Host: host}.Parse()
		if err != nil {
			t.Fatal(err)
		}
		return store.ServiceRow{Service: contract.Service{ID: "svc_1", Room: "r2", Spec: parts.Spec, State: contract.ServicePending, Reason: contract.ServiceNoCPU}, Parts: parts, Only: only}
	}
	for _, tc := range []struct {
		name    string
		service store.ServiceRow
		onPVE   int
	}{
		{"any host", waiting(""), 0},
		{"declared host is pve", waiting("pve"), 0},
		{"declared host is arch, which its room cannot use", waiting("arch"), 1},
		{"its rule names another host", waiting("", "elsewhere"), 1},
	} {
		f := newFixture(0)
		arch := f.addHost(readyHost("arch", 32*box.GiB))
		f.store.hosts[1].Rooms = []contract.RoomID{"elsewhere"}
		f.store.services = []store.ServiceRow{tc.service}
		notEngine := false
		for i := range f.store.hosts {
			f.store.hosts[i].EngineHost = &notEngine
		}
		f.scale(1)
		if got := len(f.drivers["pve"].runners); got != tc.onPVE || len(arch.runners) != 1-tc.onPVE {
			t.Errorf("%s: pve %d, arch %d", tc.name, got, len(arch.runners))
		}
	}
}
