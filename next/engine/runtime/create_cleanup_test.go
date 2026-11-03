package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type blockedCleanup struct {
	box.HostDriver
	entered chan struct{}
}

func (h blockedCleanup) RemovePod(ctx context.Context, _ contract.BoxID) error {
	close(h.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (blockedCleanup) RemoveStackVolumes(ctx context.Context, _ contract.BoxID) error {
	return ctx.Err()
}

func TestCreateCleanupRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := blockedCleanup{entered: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		cleanupCreate(ctx, h, lifecycle.BoxSpec{}, box.Forward{}, true, false, false, false)
		close(done)
	}()
	<-h.entered
	cancel()
	<-done
}
