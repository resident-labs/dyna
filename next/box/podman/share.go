package podman

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/resident-labs/dyna/box"
)

const (
	// shareUpLine is how the heartbeat says that a host's mount is up.
	shareUpLine = "dyna-share up"
	// shareDownStatus is how a host refuses to make a folder under a mount that is down.
	shareDownStatus = remoteExit(253)
	// shareUnmountableStatus is how shareNeeds says the host can never mount, whatever its wait: fusermount3 is not setuid.
	shareUnmountableStatus = remoteExit(2)
	// shareNeeds says what a host lacks for its mount, and exits 1, or 2 when no wait will help. Dyna changes no host system file itself.
	shareNeeds = `[ -c /dev/fuse ] || { echo "/dev/fuse is missing"; exit 1; }
command -v fusermount3 >/dev/null || { echo "fusermount3 is missing: install fuse3"; exit 1; }
[ "$EUID" = 0 ] || [ -u "$(command -v fusermount3)" ] || { echo "fusermount3 is not setuid, so FUSE mounts fail with Operation not permitted"; exit 2; }
grep -Eq '^[[:space:]]*user_allow_other' /etc/fuse.conf 2>/dev/null || { echo "user_allow_other is not set in /etc/fuse.conf"; exit 1; }
`
	// shareScript brings the mount at $1 up. A user service holds it, so it outlives the SSH connection and the engine, which
	// a running box relies on: a box bound to a mount that stops loses /box/shared until it restarts. So this starts a mount
	// only where there is none, and a deploy leaves a mount on its old binary. A mount that died leaves a dead entry, which
	// must go first.
	shareScript = shareNeeds + `up() { mountpoint -q -- "$1" 2>/dev/null; }
if ! up "$1"; then
  if ! systemctl --user -q is-active dyna-share.service; then
    fusermount3 -uz -- "$1" 2>/dev/null
    mkdir -p -- "$1" "$2" || exit 1
    systemd-run --user --quiet --collect --unit=dyna-share.service -- "$3" share mount --server "$4" --dir "$1" --cache "$2" || exit 1
  fi
  for _ in {1..100}; do up "$1" && break; sleep 0.1; done
  if ! up "$1"; then
    echo "the mount did not come up"
    journalctl --user -u dyna-share.service -n 5 -o cat --no-pager 2>/dev/null
    exit 1
  fi
fi
` + podmanSeesScript
	// podmanSeesScript makes sure Podman sees the mount at $1, which is up. Rootless Podman runs every container from a mount
	// namespace of its own, which its pause process holds and which starts as a copy of the host's. A mount made later reaches
	// it only when the folder's parent is a shared mount, which systemd makes of / but WSL and some containers do not. Without
	// it a box fails with "statfs ...: no such file or directory", or binds a dead copy of a mount that has since come back.
	// Both mount commands print the device of whatever is mounted at the folder, so they agree only on the same live mount.
	// Podman cannot take a mount into a namespace that exists, so a new one, made by podman system migrate, is the cure, and
	// that stops every running container. It waits until none runs here, and until then no box can start on this host. A lock
	// in the cache folder $2 keeps two box starts from renewing it one after the other, the second stopping the first's box.
	podmanSeesScript = `sees() {
  want=$(mountpoint -d -- "$1" 2>/dev/null) && [ -n "$want" ] && [ "$(podman unshare mountpoint -d -- "$1" 2>/dev/null)" = "$want" ]
}
sees "$1" && exit 0
mkdir -p -- "$2" && exec 9>"$2/podman-namespace.lock" && flock 9 || exit 1
sees "$1" && exit 0
running=$(podman ps -q) || { echo "podman ps failed"; exit 1; }
if [ -n "$running" ]; then
  echo "Podman's namespace is older than the mount and cannot see it; it is renewed once no container runs on this host"
  exit 1
fi
if ! out=$(podman system migrate 2>&1); then
  echo "podman system migrate failed"
  printf '%s\n' "$out" | tail -n 3
  exit 1
fi
sees "$1" && exit 0
echo "Podman cannot see the mount even in a new namespace"
exit 1
`
	// shareServerScript starts the server of the rooms' data on the engine's own machine. A user service holds this too, so it
	// outlives the engine, and a restart costs a mount only the moment its forward takes to come back.
	shareServerScript = `systemctl --user -q is-active dyna-share-server.service && exit 0
systemd-run --user --quiet --collect --unit=dyna-share-server.service -- "$1" share serve --rooms "$2" --listen "$3"
`
	// mountFolderScript makes the folder $1, which sits on the mount $2 if there is one.
	mountFolderScript = `if [ -n "$2" ] && ! mountpoint -q -- "$2" 2>/dev/null; then exit 253; fi; if [ ! -e "$1" ]; then mkdir -p -m 0700 -- "$1"; fi`
)

func (h *Host) checkShare(ctx context.Context) error {
	out, err := h.run(ctx, "bash", "-c", shareNeeds)
	var exit remoteExit
	if errors.As(err, &exit) {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return err
}

// EnsureShareServer starts the server on this machine unless it is running.
func EnsureShareServer(ctx context.Context, binary, rooms, socket string) error {
	out, err := exec.CommandContext(ctx, "bash", "-c", shareServerScript, "dyna-share-server", binary, rooms, socket).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureShare brings up the host's mount of the rooms' data, or says why it cannot.
func (h *Host) EnsureShare(ctx context.Context, layout box.Layout) error {
	out, err := h.run(ctx, "bash", "-c", shareScript, "dyna-share", layout.HostShareMount(), layout.HostShareCache(), layout.HostBinary(), layout.HostShareSocket())
	var exit remoteExit
	if errors.As(err, &exit) {
		return box.ShareDown{Reason: strings.TrimSpace(string(out)), Permanent: exit == shareUnmountableStatus}
	}
	return err
}

// makeMountFolder makes the folder of a writable mount, or of one that asks for it.
func (h *Host) makeMountFolder(ctx context.Context, m box.Mount) error {
	if m.ReadOnly && !m.MakeDir {
		return nil
	}
	_, err := h.run(ctx, "bash", "-c", mountFolderScript, "dyna", m.HostPath, m.OnMount)
	if errors.Is(err, shareDownStatus) {
		return box.ShareDown{Reason: "the mount is down"}
	}
	return err
}
