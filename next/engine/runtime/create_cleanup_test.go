package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type recordedCleanup struct {
	box.HostDriver
	removed []string
}

func (h *recordedCleanup) RemovePod(ctx context.Context, _ contract.BoxID) error {
	h.removed = append(h.removed, "pod:"+errText(ctx.Err()))
	return nil
}
func (h *recordedCleanup) RemoveStackVolumes(context.Context, contract.BoxID) error {
	h.removed = append(h.removed, "stack")
	return nil
}
func (h *recordedCleanup) RemoveContainer(context.Context, contract.BoxID) error {
	h.removed = append(h.removed, "container")
	return nil
}
func (h *recordedCleanup) RemoveVolume(context.Context, contract.BoxID) error {
	h.removed = append(h.removed, "volume")
	return nil
}
func (h *recordedCleanup) RemoveForward(context.Context, box.Forward) error {
	h.removed = append(h.removed, "forward")
	return nil
}
func (h *recordedCleanup) RemoveSlice(context.Context, string) error {
	h.removed = append(h.removed, "slice")
	return nil
}

func errText(err error) string {
	if err == nil {
		return "live"
	}
	return err.Error()
}

// A creation that hit its deadline still cleans up what it made, on a context the deadline does not reach.
func TestCreateCleanupRunsAfterCreationContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &recordedCleanup{}
	cleanupCreate(ctx, h, lifecycle.BoxSpec{}, box.Forward{}, true, true, true, true)
	want := []string{"pod:live", "container", "forward", "slice"}
	if len(h.removed) != len(want) {
		t.Fatalf("removed %v, want %v", h.removed, want)
	}
	for i := range want {
		if h.removed[i] != want[i] {
			t.Fatalf("removed %v, want %v", h.removed, want)
		}
	}
}
