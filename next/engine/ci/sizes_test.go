package ci

import (
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Each size is a scale set of its own, named as a workflow's runs-on names it, so GitHub assigns each job to its size.
func TestSizesMapToScaleSets(t *testing.T) {
	var names []string
	for _, size := range Sizes {
		if _, ok := box.Size(size); !ok {
			t.Fatalf("%s is not a box size", size)
		}
		names = append(names, SetName(size))
	}
	if want := []string{"dyna-small", "dyna-medium", "dyna-large", "dyna-xlarge"}; !slices.Equal(names, want) {
		t.Fatalf("sets %v, want %v", names, want)
	}
	f := newFixture(0)
	defer f.stop()
	f.tick(t)
	listening := func(name string) int {
		f.sets.mu.Lock()
		defer f.sets.mu.Unlock()
		return f.sets.listening[string(repo)+" "+name]
	}
	for _, name := range names {
		for deadline := time.Now().Add(5 * time.Second); listening(name) == 0 && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if n := listening(name); n != 1 {
			t.Errorf("%s has %d listeners", name, n)
		}
	}
}

// The listener of a size starts runners of that size, registered in that size's set.
func TestEachListenerStartsRunnersOfItsSize(t *testing.T) {
	f := newFixture(0)
	f.scaleSize(contract.BoxSizeMedium, 1)
	f.scaleSize(contract.BoxSizeXLarge, 1)
	specs := f.drivers["pve"].specs
	if len(specs) != 2 || specs[0].Size.Name != contract.BoxSizeMedium || specs[1].Size.Name != contract.BoxSizeXLarge || specs[1].Size.CPUs != 16 {
		t.Fatalf("specs %+v", specs)
	}
	if !slices.Equal(f.sets.sets, []string{"dyna-medium", "dyna-xlarge"}) {
		t.Fatalf("registered in %v", f.sets.sets)
	}
	// A medium job already has its runner, so the medium set starts no second one for it.
	f.scaleSize(contract.BoxSizeMedium, 1)
	if len(f.drivers["pve"].specs) != 2 {
		t.Fatalf("specs %d", len(f.drivers["pve"].specs))
	}
}

// --max bounds a repository's runners across its sizes.
func TestMaxBoundsRunnersAcrossSizes(t *testing.T) {
	f := newFixture(2)
	f.scaleSize(contract.BoxSizeSmall, 2)
	if capacity := f.scaleSize(contract.BoxSizeLarge, 1); capacity != 0 || len(f.drivers["pve"].runners) != 2 {
		t.Fatalf("capacity %d, runners %d", capacity, len(f.drivers["pve"].runners))
	}
}
