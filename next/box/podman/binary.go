package podman

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

// InstallBinary stages compressed bytes under their checksum, then activates them. Version remains a display identifier;
// the content checksum is the host path, so deploy staging and a new engine agree even when their build labels differ.
func (h *Host) InstallBinary(ctx context.Context, bin, version, sum string, read func(BinaryEncoding) ([]byte, error)) error {
	if version == "" || version == "." || version == ".." || filepath.Base(version) != version || strings.ContainsRune(version, '\x00') {
		return errors.New("invalid binary version")
	}
	if err := h.StageBinary(ctx, bin, sum, read); err != nil {
		return err
	}
	return h.ActivateBinary(ctx, bin, sum)
}

func binaryPath(bin, sum string) error {
	hash, err := hex.DecodeString(sum)
	if !filepath.IsAbs(bin) || err != nil || len(hash) != 32 || strings.ToLower(sum) != sum {
		return errors.New("invalid binary path or checksum")
	}
	return nil
}

type BinaryDecompressorMissing struct{ Host string }

func (e BinaryDecompressorMissing) Error() string {
	return "host " + e.Host + ": neither zstd nor gzip is available to unpack Dyna"
}

// StageBinary sends a compressed payload only if the host lacks these exact bytes. It never changes current.
// The driver selects zstd when available, otherwise gzip, before requesting cached bytes. Private temporaries allow races.
func (h *Host) StageBinary(ctx context.Context, bin, sum string, read func(BinaryEncoding) ([]byte, error)) error {
	ctx = WithRemoteCancellation(ctx)
	if err := binaryPath(bin, sum); err != nil {
		return err
	}
	probe, err := h.run(ctx, "bash", "-c", `set -eu
if test -f "$1/$2/dyna"; then sha256sum < "$1/$2/dyna" | cut -d ' ' -f 1; fi`, "dyna-probe", bin, sum)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(probe)) == sum {
		return nil
	}
	codec, err := h.run(ctx, "bash", "-c", `if command -v zstd >/dev/null && unpacked=$(printf '\050\265\057\375\040\004\041\000\000Dyna' | zstd -dc 2>/dev/null) && [ "$unpacked" = Dyna ]; then
 echo zstd
elif command -v gzip >/dev/null && unpacked=$(printf '\037\213\010\000\000\000\000\000\000\003\163\251\314\113\004\000\116\124\331\257\004\000\000\000' | gzip -dc 2>/dev/null) && [ "$unpacked" = Dyna ]; then
 echo gzip
fi`)
	if err != nil {
		return err
	}
	var encoding BinaryEncoding
	switch strings.TrimSpace(string(codec)) {
	case string(BinaryZstd):
		encoding = BinaryZstd
	case string(BinaryGzip):
		encoding = BinaryGzip
	default:
		return BinaryDecompressorMissing{Host: h.destination}
	}
	payload, err := read(encoding)
	if err != nil {
		return err
	}
	_, err = h.runInput(ctx, payload, "bash", "-c", `set -eu
dir="$1/$2"
mkdir -p -- "$dir"
temporary="$dir/.dyna.${DYNA_START_DIR##*/}"
printf '%s\0' "$temporary" >> "$DYNA_START_DIR/temps"
mkdir -m 700 -- "$temporary"
trap 'rm -rf -- "$temporary"' EXIT HUP INT TERM
"$3" -dc > "$temporary/dyna"
test "$(sha256sum < "$temporary/dyna" | cut -d ' ' -f 1)" = "$2"
chmod 0755 "$temporary/dyna"
mv -f -- "$temporary/dyna" "$dir/dyna"`, "dyna-install", bin, sum, string(encoding))
	return err
}

// ActivateBinary verifies the staged object before atomically switching current. Only the engine calls this after restart.
func (h *Host) ActivateBinary(ctx context.Context, bin, sum string) error {
	ctx = WithRemoteCancellation(ctx)
	if err := binaryPath(bin, sum); err != nil {
		return err
	}
	_, err := h.run(ctx, "bash", "-c", `set -eu
cd -- "$1"
test "$(sha256sum < "$2/dyna" | cut -d ' ' -f 1)" = "$2"
linkdir="$PWD/.current.${DYNA_START_DIR##*/}"
printf '%s\0' "$linkdir" >> "$DYNA_START_DIR/temps"
mkdir -m 700 -- "$linkdir"
trap 'rm -rf -- "$linkdir"' EXIT HUP INT TERM
ln -s -- "$2" "$linkdir/current"
mv -Tf -- "$linkdir/current" current`, "dyna-link", bin, sum)
	return err
}
