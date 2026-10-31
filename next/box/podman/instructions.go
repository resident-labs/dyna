package podman

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

type InstructionFile struct {
	Path string
	Data []byte
}

// SyncInstructions checks and atomically replaces existing instruction files in one command.
func (h *Host) SyncInstructions(ctx context.Context, files []InstructionFile) error {
	if len(files) == 0 {
		return nil
	}
	var input bytes.Buffer
	args := []string{"bash", "-c", `set -eu
while [ "$#" -gt 0 ]; do
 path=$1; sum=$2; shift 2
 IFS= read -r data || exit 1
 if [ -f "$path" ] && [ "$(sha256sum "$path" | cut -d' ' -f1)" = "$sum" ]; then continue; fi
 mkdir -p "$(dirname "$path")"
 tmp=$(mktemp "${path}.XXXXXX")
 trap 'rm -f "$tmp"' EXIT
 printf '%s' "$data" | base64 -d > "$tmp"
 chmod 600 "$tmp"
 mv -f "$tmp" "$path"
 trap - EXIT
done`, "dyna-instructions"}
	for _, file := range files {
		sum := sha256.Sum256(file.Data)
		args = append(args, file.Path, fmt.Sprintf("%x", sum))
		input.WriteString(base64.StdEncoding.EncodeToString(file.Data))
		input.WriteByte('\n')
	}
	_, err := h.runInput(ctx, input.Bytes(), args...)
	return err
}
