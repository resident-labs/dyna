package engine

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// fakeRunners stands for the CI pool: the runners it has placed and the jobs that wait for a node.
type fakeRunners struct {
	mu      sync.Mutex
	placed  []box.Box
	waiting []store.LifecycleBox
}

func (f *fakeRunners) Runners() ([]box.Box, []store.LifecycleBox) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.placed), slices.Clone(f.waiting)
}

func (f *fakeRunners) Hold(host contract.HostName, drain func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.ContainsFunc(f.placed, func(b box.Box) bool { return b.Host == host }) {
		return &store.HostHeldError{Host: host, Held: []string{"CI runner"}}
	}
	return drain()
}

// A job no host takes asks the room's pool for a node of its size, as a box does. The node has ci on, holds while its runner
// runs, and goes after the pool's idle time once the runner is gone.
func TestAJobNoHostTakesGetsANodeFromTheRoomsPool(t *testing.T) {
	r := setupPool(t)
	runners := &fakeRunners{}
	r.s.reconciler.SetRunners(runners)
	r.addPool(2, false)
	large, _ := box.Size(contract.BoxSizeLarge)
	runners.waiting = []store.LifecycleBox{{Box: box.Box{ID: "dyna-ci-wait-owner-repo-large-0", Room: r.room, Image: box.RunnerImage, Size: large, State: contract.BoxPending, Reason: string(box.PendingPoolNode)}, Want: store.WantRun, Only: []string{"boats"}}}
	r.pass(2)
	if r.calls("create") != 1 {
		t.Fatalf("%d creates", r.calls("create"))
	}
	nodes := r.nodeRows()
	if len(nodes) != 1 || nodes[0].State != box.HostReady || !nodes[0].CI {
		t.Fatalf("nodes %+v", nodes)
	}
	node := nodes[0].Name
	// The runner starts there. However long its job runs, its node stays.
	runners.mu.Lock()
	runners.waiting, runners.placed = nil, []box.Box{{ID: "dyna-ci-node-1", Host: node, Image: box.RunnerImage, Size: large, State: contract.BoxReady}}
	runners.mu.Unlock()
	r.pass(1)
	r.advance(2 * time.Hour)
	r.pass(2)
	if r.calls("destroy") != 0 {
		t.Fatal("a node with a running runner was destroyed")
	}
	if _, err := r.s.reconciler.RemoveHost(r.ctx, node, false); !errors.As(err, new(*store.HostHeldError)) {
		t.Fatalf("removed a host with a runner: %v", err)
	}
	// The job ended. The node goes after the idle time, as one a box held does.
	runners.mu.Lock()
	runners.placed = nil
	runners.mu.Unlock()
	r.pass(1)
	if r.calls("destroy") != 0 {
		t.Fatal("the node went before its idle time")
	}
	r.advance(31 * time.Minute)
	r.pass(2)
	if r.calls("destroy") != 1 || len(r.nodeRows()) != 0 {
		t.Fatalf("%d destroys, nodes %+v", r.calls("destroy"), r.nodeRows())
	}
}

// A runner counts against a host as a box does, so a box does not land where runners fill it.
func TestRunnersCountAgainstBoxPlacement(t *testing.T) {
	r := setupPool(t)
	runners := &fakeRunners{}
	r.s.reconciler.SetRunners(runners)
	// The host has 32 CPUs, four times over: 128. Eight xlarge runners take all of it.
	xlarge, _ := box.Size(contract.BoxSizeXLarge)
	for i := range 8 {
		runners.placed = append(runners.placed, box.Box{ID: contract.BoxID("dyna-ci-host-" + string(rune('a'+i))), Host: "host", Image: box.RunnerImage, Size: xlarge, State: contract.BoxReady})
	}
	session := r.start("worker")
	r.pass(1)
	if b := r.box(session); b.Host != "" || b.State != contract.BoxPending {
		t.Fatalf("a box landed beside runners that fill the host: %+v", b)
	}
	runners.mu.Lock()
	runners.placed = nil
	runners.mu.Unlock()
	r.pass(1)
	if b := r.box(session); b.Host != "host" {
		t.Fatalf("the box did not land once the runners were gone: %+v", b)
	}
}
