package runtime

import (
	"context"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/ci"
)

// ciHost gives the CI pool a host's runner operations.
type ciHost struct {
	*podman.Host
	name  contract.HostName
	boxes *Boxes
}

// EnsureEgress installs the engine binary first, because the proxy is that binary.
func (h ciHost) EnsureEgress(ctx context.Context, image string) error {
	if err := h.boxes.installBinary(ctx, h.Host); err != nil {
		return err
	}
	return h.Host.EnsureEgress(ctx, image, h.boxes.Layout.HostBin(), h.boxes.binSum)
}

// StartRunner gives the runner the host's cache, the one its boxes mount at /box/cache, and their compiler cache.
func (h ciHost) StartRunner(ctx context.Context, spec box.RunnerSpec) error {
	if err := h.boxes.installBinary(ctx, h.Host); err != nil {
		return err
	}
	h.boxes.startBuildCache(ctx, h.name, h.Host)
	spec.Cache, spec.BuildCache = h.boxes.Layout.HostCache(), h.boxes.Layout.HostBuildCacheSocketFolder()
	return h.Host.StartRunner(ctx, spec)
}

func (b *Boxes) ciDriver(ctx context.Context, name contract.HostName) (ci.Driver, error) {
	h, err := b.host(ctx, name)
	if err != nil {
		return nil, err
	}
	return ciHost{Host: h, name: name, boxes: b}, nil
}
