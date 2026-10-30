package podman

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// InstallBinary installs one version and atomically switches the current link.
func (h *Host) InstallBinary(ctx context.Context, hostData, version string, binary []byte) error {
	if !filepath.IsAbs(hostData) || version == "." || version == ".." || version == "" || filepath.Base(version) != version || strings.ContainsRune(version, '\x00') {
		return errors.New("invalid binary path or version")
	}
	_, err := h.runInput(ctx, binary, "bash", "-c", `set -eu
dir="$1/bin/$2"
mkdir -p -- "$dir"
tmp=$(mktemp "$dir/.dyna.XXXXXX")
linkdir=
trap 'rm -f -- "$tmp"; if [ -n "$linkdir" ]; then rm -rf -- "$linkdir"; fi' EXIT
cat > "$tmp"
chmod 0755 "$tmp"
if ! test -f "$dir/dyna" || [ "$(sha256sum < "$tmp")" != "$(sha256sum < "$dir/dyna")" ]; then mv -f -- "$tmp" "$dir/dyna"; fi
cd -- "$1/bin"
linkdir=$(mktemp -d .current.XXXXXX)
ln -s -- "$2" "$linkdir/current"
mv -Tf -- "$linkdir/current" current`, "dyna-install", hostData, version)
	return err
}
