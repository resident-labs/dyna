package podman

import (
	"context"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestNamespaceDeleteDoesNotRestoreOrCancelForwards(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	layout := box.Layout{HostData: h.dataDir}
	old := box.Forward{Box: "a", HostPath: layout.HostSocket("a"), EnginePath: "/engine/a"}
	other := box.Forward{Box: "b", HostPath: layout.HostSocket("b"), EnginePath: "/engine/b"}
	// An un-restored mux is important: generic run would restore these before
	// ever checking the requested deletion epoch, unlinking their endpoints.
	h.forwards[old] = struct{}{}
	h.forwards[other] = struct{}{}
	epoch, err := h.BoxEpoch(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveBox(context.Background(), "a", epoch, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, args := range r.calls {
		if slices.Contains(args, "-O") || slices.Contains(args, "-S") || !slices.Contains(args, "ControlPath=none") {
			t.Fatalf("namespace read/delete can mutate forwarding before or after epoch check: %v", args)
		}
	}
	if _, ok := h.forwards[old]; ok {
		t.Fatal("completed deletion still wants the removed forward")
	}
	if _, ok := h.forwards[other]; !ok {
		t.Fatal("deletion forgot an unrelated box's forward")
	}
}
