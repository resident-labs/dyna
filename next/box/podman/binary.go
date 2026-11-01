package podman

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// InstallBinary sends the engine bytes only when the host lacks the sum. It always switches the current link.
func (h *Host) InstallBinary(ctx context.Context, bin, version, sum string, read func() ([]byte, error)) error {
	if !filepath.IsAbs(bin) || version == "." || version == ".." || version == "" || filepath.Base(version) != version || strings.ContainsRune(version, '\x00') {
		return errors.New("invalid binary path or version")
	}
	probe, err := h.run(ctx, "bash", "-c", `set -eu
if test -f "$1/$2/dyna"; then sha256sum < "$1/$2/dyna" | cut -d ' ' -f 1; fi`, "dyna-probe", bin, version)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(probe)) == sum {
		_, err = h.run(ctx, "bash", "-c", `set -eu
cd -- "$1"
linkdir=$(mktemp -d .current.XXXXXX)
trap 'rm -rf -- "$linkdir"' EXIT
ln -s -- "$2" "$linkdir/current"
mv -Tf -- "$linkdir/current" current`, "dyna-link", bin, version)
		return err
	}
	binary, err := read()
	if err != nil {
		return err
	}
	_, err = h.runInput(ctx, binary, "bash", "-c", `set -eu
dir="$1/$2"
mkdir -p -- "$dir"
tmp=$(mktemp "$dir/.dyna.XXXXXX")
linkdir=
trap 'rm -f -- "$tmp"; if [ -n "$linkdir" ]; then rm -rf -- "$linkdir"; fi' EXIT
cat > "$tmp"
chmod 0755 "$tmp"
if ! test -f "$dir/dyna" || [ "$(sha256sum < "$tmp")" != "$(sha256sum < "$dir/dyna")" ]; then mv -f -- "$tmp" "$dir/dyna"; fi
cd -- "$1"
linkdir=$(mktemp -d .current.XXXXXX)
ln -s -- "$2" "$linkdir/current"
mv -Tf -- "$linkdir/current" current`, "dyna-install", bin, version)
	return err
}
