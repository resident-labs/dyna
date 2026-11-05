package ci

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// A runner goes where a box would: the ready host with ci on and the most free memory. No repository names its hosts.
func TestPlacementPicksACIOnHostByFreeMemory(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].MemBytes = 32 * box.GiB
	big := f.addHost(readyHost("big", 128*box.GiB))
	off := readyHost("biggest", 512*box.GiB)
	off.CI = false
	biggest := f.addHost(off)
	f.scale(1)
	if len(big.runners) != 1 || len(f.drivers["pve"].runners) != 0 || len(biggest.runners) != 0 {
		t.Fatalf("big %d, pve %d, ci off %d", len(big.runners), len(f.drivers["pve"].runners), len(biggest.runners))
	}
	// Two runners share a host as long as admission takes them.
	f.scale(2)
	if len(big.runners) != 2 {
		t.Fatalf("big %d, pve %d", len(big.runners), len(f.drivers["pve"].runners))
	}
}

func TestACIOffHostIsSkippedEvenWhenItIsTheOnlyOneThatFits(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].CI = false
	f.addHost(readyHost("small", 4*box.GiB)) // below a large runner's memory, even at four times over
	f.store.hosts[1].CPUs = 1
	if capacity := f.scale(1); capacity != 0 || len(f.sets.names) != 0 {
		t.Fatalf("capacity %d, registered %v", capacity, f.sets.names)
	}
}

// A room whose rule names a pool keeps its runners on that pool's nodes, as it keeps its boxes. When none fits, the job waits
// for a node, the node pools hear of it, and GitHub still sees capacity, so the job is assigned. Once the node is up, the
// next poll places the runner there.
func TestAJobNoHostTakesWaitsForANodeOfTheRoomsPool(t *testing.T) {
	f := newFixture(0)
	f.store.repos[0].Only = []string{"boats"}
	f.store.pools = []store.Pool{{Name: "boats", Max: 2}}
	if capacity := f.scale(1); capacity != 2 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("capacity %d, runners on pve %d", capacity, len(f.drivers["pve"].runners))
	}
	placed, waiting := f.pool.Runners()
	if len(placed) != 0 || len(waiting) != 1 || f.kicks != 1 {
		t.Fatalf("placed %v, waiting %+v, kicks %d", placed, waiting, f.kicks)
	}
	if w := waiting[0]; w.Room != "room" || w.Size.Name != large || !slices.Equal(w.Only, []string{"boats"}) || w.Want != store.WantRun || w.Reason != string(box.PendingPoolNode) {
		t.Fatalf("waiting %+v", w)
	}
	f.scale(1)
	if f.kicks != 1 {
		t.Fatal("the same wait kicked the node pools again")
	}
	node := readyHost("boats-1", 32*box.GiB)
	node.Pool, node.Rooms = "boats", []contract.RoomID{"room"}
	d := f.addHost(node)
	if capacity := f.scale(1); len(d.runners) != 1 || capacity < 1 {
		t.Fatalf("node %d runners, capacity %d", len(d.runners), capacity)
	}
	placed, waiting = f.pool.Runners()
	if len(placed) != 1 || placed[0].Host != "boats-1" || len(waiting) != 0 || f.kicks != 2 {
		t.Fatalf("placed %v, waiting %+v, kicks %d", placed, waiting, f.kicks)
	}
}

// A per-session node serves the one job it was made for, so the job keeps its name from one poll to the next.
func TestAPerSessionNodeTakesTheJobItWasMadeFor(t *testing.T) {
	f := newFixture(0)
	f.store.repos[0].Only = []string{"ships"}
	f.store.pools = []store.Pool{{Name: "ships", Max: 4, PerSession: true}}
	f.scale(2)
	_, waiting := f.pool.Runners()
	if len(waiting) != 2 || waiting[0].ID == waiting[1].ID {
		t.Fatalf("waiting %+v", waiting)
	}
	node := readyHost("ships-1", 32*box.GiB)
	node.Pool, node.Box = "ships", waiting[1].ID
	d := f.addHost(node)
	f.scale(2)
	_, still := f.pool.Runners()
	if len(d.runners) != 1 || len(still) != 1 || still[0].ID != waiting[0].ID {
		t.Fatalf("node %d runners, still waiting %+v", len(d.runners), still)
	}
}

// The node pools drain a host only when no runner is on it, and a runner never starts on a host that drains.
func TestANodeWithARunnerIsNotDrainedAndADrainedHostTakesNone(t *testing.T) {
	f := newFixture(0)
	f.scale(1)
	drained := false
	err := f.pool.Hold("pve", func() error { drained = true; return nil })
	var held *store.HostHeldError
	if !errors.As(err, &held) || drained {
		t.Fatalf("drained a host with a runner: %v", err)
	}
	f.drivers["pve"].runners = nil
	f.scale(0)
	if err := f.pool.Hold("pve", func() error { f.store.hosts[0].State = box.HostDraining; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.claim(context.Background(), runnerBox("late", "pve", box.Box{}.Size), ""); err == nil {
		t.Fatal("claimed a runner on a draining host")
	}
}

// After a deploy, or on a new host, the host with the most free memory may still have to build the runner image. A runner goes
// to a host that has it, and a host that must build takes one only when none with the image has room.
func TestPlacementPrefersAHostWithTheRunnerImage(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].MemBytes = 128 * box.GiB // pve has the most free memory, and no image
	ready := f.addHost(readyHost("desktop", 32*box.GiB))
	f.pool.images = map[imageKey]string{{"desktop", ""}: "image"}
	f.scale(1)
	if len(ready.runners) != 1 || len(f.drivers["pve"].runners) != 0 || f.drivers["pve"].builds != 0 {
		t.Fatalf("desktop %d, pve %d runners, %d builds", len(ready.runners), len(f.drivers["pve"].runners), f.drivers["pve"].builds)
	}
	// With no room left where the image is, the building host takes the job, so a new host still gets its image.
	for range box.RunnerSlots {
		f.scale(box.RunnerSlots + 1)
	}
	if len(f.drivers["pve"].runners) == 0 || f.drivers["pve"].builds != 1 {
		t.Fatalf("pve %d runners, %d builds", len(f.drivers["pve"].runners), f.drivers["pve"].builds)
	}
}

// A host that must build its image does it beside placement, without p.mu, and is preferred once it is done.
func TestBuildAheadDoesNotHoldThePoolLock(t *testing.T) {
	f := newFixture(0)
	release := make(chan struct{})
	f.drivers["pve"].gate = release
	f.tickNoWait(t)
	// A job whose only host is still building waits for a later poll, with no second build.
	done := make(chan struct{})
	go func() { f.scale(1); close(done) }()
	<-done // returns while the build is still running, so p.mu is free
	if len(f.drivers["pve"].runners) != 0 {
		t.Fatal("a runner started on a host that is still building")
	}
	close(release)
	f.stop() // the build outlives the tick and finishes
	if f.pool.images[imageKey{"pve", ""}] != "image" || f.drivers["pve"].builds != 1 {
		t.Fatalf("images %v, builds %d", f.pool.images, f.drivers["pve"].builds)
	}
}

// Image preparation/SSH happens between the survey and claim. A newly committed service reservation in that interval must
// be seen before another runner is published, otherwise the old survey and the service update spend the same memory twice.
func TestRunnerClaimRechecksServiceReservationsAfterItsSurvey(t *testing.T) {
	f := newFixture(0)
	size, _ := box.Size(contract.BoxSizeLarge)
	for i := range 3 {
		f.pool.placed = append(f.pool.placed, runnerBox(fmt.Sprintf("existing-%d", i), "pve", size))
	}
	f.store.hosts[0].Services = []box.ServiceHold{{ID: "svc_1", CPUMilli: 1000, MemBytes: 16 * box.GiB}}
	if err := f.pool.claim(context.Background(), runnerBox("new", "pve", size), ""); err == nil {
		t.Fatal("runner claimed capacity already reserved by a service since the survey")
	}
	if len(f.pool.placed) != 3 {
		t.Fatalf("refused runner was published: %+v", f.pool.placed)
	}
}
