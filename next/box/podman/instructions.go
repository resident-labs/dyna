package podman

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// SyncInstruction compares the remote file and replaces it atomically if needed.
func (h *Host) SyncInstruction(ctx context.Context, path string, data []byte) error {
	sum := sha256.Sum256(data)
	_, err := h.runInput(ctx, data, "bash", "-c", `set -eu
path=$1
sum=$2
mkdir -p "$(dirname "$path")"
if [ -f "$path" ] && [ "$(sha256sum "$path" | cut -d' ' -f1)" = "$sum" ]; then exit 0; fi
tmp=$(mktemp "${path}.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
chmod 600 "$tmp"
mv -f "$tmp" "$path"
`, "dyna-instructions", path, fmt.Sprintf("%x", sum))
	return err
}
