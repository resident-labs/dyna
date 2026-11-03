package ci

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// layers is the ci-runner images of rooms as the pool reads them: ids by room, where a test changes one to save a new image, and
// rooms whose image cannot be read.
type layers struct {
	id     string
	others map[contract.RoomID]string
	broken map[contract.RoomID]error
}

func (l *layers) idOf(room contract.RoomID) (string, error) {
	if err := l.broken[room]; err != nil {
		return "", err
	}
	if room == "room" {
		return l.id, nil
	}
	return l.others[room], nil
}

func (l *layers) read(room contract.RoomID) (*box.RunnerLayer, error) {
	id, err := l.idOf(room)
	if id == "" || err != nil {
		return nil, err
	}
	return &box.RunnerLayer{Room: room, ID: id, Context: []byte("tar")}, nil
}

// use makes the pool read these.
func (l *layers) use(p *Pool) {
	p.LayerID, p.Layer = l.idOf, l.read
}

func TestARunnerStartsOnTheRoomsImageOrTheSharedOne(t *testing.T) {
	for name, tc := range map[string]struct{ id, want string }{"room image": {"abc12345", "layer-abc12345"}, "none": {"", "image"}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(0)
			(&layers{id: tc.id}).use(f.pool)
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
	(&layers{id: "abc12345"}).use(f.pool)
	f.pool.images = map[imageKey]string{{"pve", ""}: "image", {"desktop", ""}: "image", {"desktop", "abc12345"}: "layer-abc12345"}
	f.scale(1)
	if len(desktop.runners) != 1 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("desktop %d, pve %d runners", len(desktop.runners), len(f.drivers["pve"].runners))
	}
}

func TestAFailedLayerBuildFallsBackToTheSharedImageAndShowsAProblem(t *testing.T) {
	f := newFixture(0)
	f.drivers["pve"].failLayer = errors.New("apt: no such package")
	(&layers{id: "abc12345"}).use(f.pool)
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
	(&layers{id: "def67890"}).use(f.pool)
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
	saved.use(f.pool)
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

// podman's BuildError says only "podman build failed" and carries the output apart, so the problem must read the output.
func TestAFailedBuildsProblemCarriesTheBuildOutput(t *testing.T) {
	f := newFixture(0)
	f.drivers["pve"].failLayer = &podman.BuildError{Tail: "Step 2/3\nE: Unable to locate package nonesuch\n"}
	(&layers{id: "abc12345"}).use(f.pool)
	f.scale(1)
	problems := f.pool.Problems()["pve"]
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "Unable to locate package nonesuch") || strings.Contains(problems[0].Reason, "\n") {
		t.Fatalf("problems %+v", problems)
	}
}

// One room's hanging build stops at its own limit, so the shared image and the other rooms' layers on the host still build, and
// the hang is a typed problem.
func TestAHangingLayerBuildTimesOutAlone(t *testing.T) {
	f := newFixture(0)
	f.store.repos = append(f.store.repos, store.CIRepo{Repo: "owner/other", Room: "other"})
	d := f.drivers["pve"]
	d.hangLayer = true
	f.pool.LayerLimit = 20 * time.Millisecond
	(&layers{id: "abc12345", others: map[contract.RoomID]string{"other": "def67890"}}).use(f.pool)
	f.tick(t)
	problems := f.pool.Problems()["pve"]
	if len(problems) != 2 || !strings.Contains(problems[0].Reason, "took longer than") || !strings.Contains(problems[1].Reason, "took longer than") {
		t.Fatalf("problems %+v", problems)
	}
	if len(d.layerBuilds) != 2 || f.pool.imageFor("pve", &box.RunnerLayer{ID: "abc12345"}) != "image" {
		t.Fatalf("layer builds %v", d.layerBuilds)
	}
}

// A build that failed is not tried again until retryAfter has passed, whether the layer failed (the host fell back) or the
// shared image did too.
func TestAFailedBuildIsRetriedEveryFiveMinutes(t *testing.T) {
	for name, fail := range map[string]func(*fakeDriver){
		"layer":  func(d *fakeDriver) { d.failLayer = errors.New("boom") },
		"shared": func(d *fakeDriver) { d.failShared = errors.New("no network") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(0)
			d := f.drivers["pve"]
			fail(d)
			(&layers{id: "abc12345"}).use(f.pool)
			f.tick(t)
			first := d.builds
			f.now = f.now.Add(retryAfter - time.Second)
			f.tick(t)
			if d.builds != first {
				t.Fatalf("built again after %d builds, before the interval", first)
			}
			f.now = f.now.Add(2 * time.Second)
			f.tick(t)
			if d.builds == first {
				t.Fatal("no retry after the interval")
			}
		})
	}
}

// One room's unreadable image is that room's problem: the other rooms and the lease go on, and its jobs use the shared image.
func TestAnUnreadableRoomImageStopsNoOtherRoom(t *testing.T) {
	f := newFixture(0)
	f.store.repos = append(f.store.repos, store.CIRepo{Repo: "owner/other", Room: "other"})
	(&layers{id: "abc12345", broken: map[contract.RoomID]error{"other": errors.New("disk I/O error")}}).use(f.pool)
	f.tick(t)
	if len(f.api.leases) == 0 {
		t.Fatal("the lease was not renewed")
	}
	if got := f.drivers["pve"].layerBuilds; len(got) != 1 || got[0] != "abc12345" {
		t.Fatalf("builds %v", got)
	}
	problems := f.pool.Problems()["pve"]
	if len(problems) != 1 || problems[0].Room != "other" || !strings.Contains(problems[0].Reason, "disk I/O error") {
		t.Fatalf("problems %+v", problems)
	}
}

// A room that drops its ci-runner image loses its problem and its images, on the pool and on the host.
func TestARoomThatDropsItsImageLeavesNoProblemOrImage(t *testing.T) {
	f := newFixture(0)
	d := f.drivers["pve"]
	d.failLayer = errors.New("boom")
	saved := &layers{id: "abc12345"}
	saved.use(f.pool)
	f.tick(t)
	if len(f.pool.Problems()["pve"]) != 1 {
		t.Fatalf("problems %+v", f.pool.Problems())
	}
	saved.id = ""
	f.tick(t)
	if len(f.pool.Problems()["pve"]) != 0 {
		t.Fatalf("problems %+v", f.pool.Problems())
	}
	if last := d.pruned[len(d.pruned)-1]; len(last) != 0 {
		t.Fatalf("pruned keeping %v", last)
	}
	for key := range f.pool.fellBack {
		t.Fatalf("a failure of %v stayed", key)
	}
}
