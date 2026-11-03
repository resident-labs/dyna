package podman

import (
	"context"

	"github.com/resident-labs/dyna/box"
)

// buildCacheScript starts the host's compiler cache at $3 on the socket $4 unless it runs. A user service holds it, so it
// outlives the SSH connection and the engine, and a deploy leaves it on its old binary. The socket's folder $2 comes first,
// open to all, because a box or runner mounts it whether the cache starts or not, and a runner's user owns nothing on the host.
const buildCacheScript = `mkdir -p -- "$2" && chmod 0755 -- "$2" || exit 1
systemctl --user -q is-active dyna-build-cache.service && exit 0
systemd-run --user --quiet --collect --unit=dyna-build-cache.service -- "$1" build-cache serve --dir "$3" --listen "$4"
`

// EnsureBuildCache starts the host's compiler cache unless it is running.
func (h *Host) EnsureBuildCache(ctx context.Context, layout box.Layout) error {
	_, err := h.run(ctx, "bash", "-c", buildCacheScript, "dyna-build-cache", layout.HostBinary(), layout.HostBuildCacheSocketFolder(), layout.HostBuildCache(), layout.HostBuildCacheSocket())
	return err
}
