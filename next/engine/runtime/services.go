package runtime

import (
	"context"

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
