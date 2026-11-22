package runtime

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/contract"
)

// ServiceDriver gives the reconciler a host's service operations over the host's SSH connection.
func (b *Boxes) ServiceDriver(ctx context.Context, name contract.HostName) (box.ServiceDriver, error) {
	return b.host(ctx, name)
}

// ServiceRoomImage builds a room image on a host as a box's image is built, and returns its tag.
func (b *Boxes) ServiceRoomImage(ctx context.Context, name contract.HostName, room contract.RoomID, image string) (string, error) {
	h, err := b.host(ctx, name)
	if err != nil {
		return "", err
	}
	base, err := h.BuildBase(ctx, images.BaseContainerfile)
	if err != nil {
		return "", err
	}
	tag, _, err := b.toolchain(ctx, h, name, room, image, base)
	return tag, err
}

func (b *Boxes) serviceForward(id contract.ServiceID) box.Forward {
	return box.Forward{
		Service:    id,
		HostPath:   b.Layout.HostServiceSocket(id),
		EnginePath: b.Sockets.ServiceSocketPath(id),
	}
}

// EnsureFeedSocket opens the engine-side listener for a feed service and forwards it onto host, returning the host
// folder that the service quadlet bind-mounts read-only.
func (b *Boxes) EnsureFeedSocket(ctx context.Context, name contract.HostName, id contract.ServiceID, room contract.RoomID, svcName contract.ServiceName) (string, error) {
	if b.Sockets == nil {
		return "", errors.New("engine sockets unavailable")
	}
	if err := b.Sockets.AddService(id, room, svcName); err != nil {
		return "", err
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return "", err
	}
	if err := h.AddForward(ctx, b.serviceForward(id)); err != nil {
		return "", err
	}
	return b.Layout.HostServiceSocketFolder(id), nil
}

// RemoveFeedSocket cancels the host forward (when host is named) and closes the engine-side listener for a feed service.
func (b *Boxes) RemoveFeedSocket(ctx context.Context, name contract.HostName, id contract.ServiceID) error {
	var hostErr error
	if name != "" && b.Sockets != nil {
		if h, err := b.host(ctx, name); err == nil {
			hostErr = h.RemoveForward(ctx, b.serviceForward(id))
		}
	}
	var sockErr error
	if b.Sockets != nil {
		sockErr = b.Sockets.RemoveService(id)
	}
	return errors.Join(hostErr, sockErr)
}
