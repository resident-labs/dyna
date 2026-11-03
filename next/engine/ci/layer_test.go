package ci

import (
	"errors"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// layers is a room's ci-runner image as the pool reads it. A test changes id to save a new one.
type layers struct{ id string }

func (l *layers) read(contract.RoomID) (*box.RunnerLayer, error) {
	if l.id == "" {
		return nil, nil
	}
	return &box.RunnerLayer{Room: "room", ID: l.id, Context: []byte("tar")}, nil
}

func TestARunnerStartsOnTheRoomsImageOrTheSharedOne(t *testing.T) {
	for name, tc := range map[string]struct{ id, want string }{"room image": {"abc12345", "layer-abc12345"}, "none": {"", "image"}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(0)
			f.pool.Layer = (&layers{id: tc.id}).read
			f.scale(1)
			if specs := f.drivers["pve"].specs; len(specs) != 1 || specs[0].Image != tc.want {
				t.Fatalf("specs %+v, want image %s", specs, tc.want)
			}
		})
	}
}

func TestPlacementPrefersAHostWithTheRoomsImage(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].MemBytes = 128 * box.GiB // pve has the most free memory, and only the shared image
	desktop := f.addHost(readyHost("desktop", 32*box.GiB))
	f.pool.Layer = (&layers{id: "abc12345"}).read
	f.pool.images = map[imageKey]string{{"pve", ""}: "image", {"desktop", ""}: "image", {"desktop", "abc12345"}: "layer-abc12345"}
	f.scale(1)
	if len(desktop.runners) != 1 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("desktop %d, pve %d runners", len(desktop.runners), len(f.drivers["pve"].runners))
	}
}

func TestAFailedLayerBuildFallsBackToTheSharedImageAndShowsAProblem(t *testing.T) {
	f := newFixture(0)
	f.drivers["pve"].failLayer = errors.New("apt: no such package")
	f.pool.Layer = (&layers{id: "abc12345"}).read
	f.scale(1)
	if specs := f.drivers["pve"].specs; len(specs) != 1 || specs[0].Image != "image" {
		t.Fatalf("specs %+v", f.drivers["pve"].specs)
	}
	problems := f.pool.Problems()["pve"]
	if len(problems) != 1 || problems[0].Room != "room" || problems[0].Image != "abc12345" || !strings.Contains(problems[0].Reason, "no such package") {
		t.Fatalf("problems %+v", f.pool.Problems())
	}
	// The host is ready for the repository on the shared image, so placement does not wait on a build that keeps failing.
	if f.pool.imageFor("pve", &box.RunnerLayer{ID: "abc12345"}) != "image" {
		t.Fatal("a host that fell back counts as not ready")
	}
	// A fixed save builds again, and the problem goes.
	f.drivers["pve"].failLayer = nil
	f.pool.Layer = (&layers{id: "def67890"}).read
	f.scale(2)
	if specs := f.drivers["pve"].specs; len(specs) != 2 || specs[1].Image != "layer-def67890" || len(f.pool.Problems()["pve"]) != 0 {
		t.Fatalf("specs %+v problems %+v", specs, f.pool.Problems())
	}
}

// A host that missed a save, because it was offline or new, builds the room's current image at its next build and not the one
// the room had when the host went away.
func TestAHostThatMissedASaveBuildsTheCurrentImage(t *testing.T) {
	f := newFixture(0)
	saved := &layers{id: "aaaa1111"}
	f.pool.Layer = saved.read
	f.tick(t)
	if got := f.drivers["pve"].layerBuilds; len(got) != 1 || got[0] != "aaaa1111" {
		t.Fatalf("pve built %v", got)
	}
	// The room saves again. A second host joins afterwards, and pve is still on the old image until its next pass.
	saved.id = "bbbb2222"
	late := f.addHost(readyHost("late", 32*box.GiB))
	f.tick(t)
	if len(late.layerBuilds) != 1 || late.layerBuilds[0] != "bbbb2222" {
		t.Fatalf("late built %v, want only the current image", late.layerBuilds)
	}
	if got := f.drivers["pve"].layerBuilds; len(got) != 2 || got[1] != "bbbb2222" {
		t.Fatalf("pve built %v, want the new save too", got)
	}
	// A pass with nothing new builds nothing.
	f.tick(t)
	if len(late.layerBuilds) != 1 || len(f.drivers["pve"].layerBuilds) != 2 {
		t.Fatalf("rebuilt: late %v, pve %v", late.layerBuilds, f.drivers["pve"].layerBuilds)
	}
}
