package podman

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box"
)

// What agy needs on a host, and how one host gets it from another. The signed-in folder is the owner's choice to share across hosts:
// it travels as an opaque tar stream that the engine only carries, never parses, logs or stores, and each host lives on its own
// copy afterwards. Every failure here is a fixed text, because the bytes of a failed pack would otherwise ride in the error.

// AgyState is what a host has of agy, from a stat-only probe, and its machine kind, which decides whether the program can be copied.
type AgyState struct {
	HasBinary, HasAccount bool
	Arch                  string // `uname -s -m`
	BinarySum             string // sha256 of the program, when there is one
}

// agyStateScript prints what is at $1 (the agy folder). It stats and hashes the program and never opens the account folder.
const agyStateScript = `if [ -f "$1/antigravity" ] && [ -x "$1/antigravity" ]; then echo dyna-agy-binary; echo "dyna-agy-sum $(sha256sum < "$1/antigravity" | cut -d ' ' -f 1)"; fi
if [ -d "$1/accounts/main" ]; then echo dyna-agy-account; fi
echo "dyna-agy-arch $(uname -s -m)"
`

// agyPackScript writes a tar of the members named after $1 (paths below $1) to stdout, leaving out the conversation databases and the
// logs by path. Its stderr is dropped so that only the archive is on stdout.
const agyPackScript = `set -eu
cd -- "$1"
shift
exec tar -c -f - --exclude=accounts/main/antigravity-acp/conversations --exclude=accounts/main/antigravity-acp/log -- "$@" 2>/dev/null`

// agyUnpackScript extracts the tar on stdin under a temporary folder in $1, checks the program's checksum $2 (or "-" for none), and
// moves each piece into place, so a half copy is never at the final path. The account folder is 0700. A present account folder is left
// alone unless $3 is 1, and is then put back if the move fails.
const agyUnpackScript = `set -eu
dir=$1 want=$2 replace=$3
mkdir -p -m 0700 -- "$dir"
exec 9> "$dir/.seed.lock"
flock -x 9
tmp=$(mktemp -d "$dir/.seed.XXXXXX")
trap 'rm -rf -- "$tmp"' EXIT HUP INT TERM
tar -x -p --no-same-owner -C "$tmp" -f -
if [ "$want" != - ]; then
 test "$(sha256sum < "$tmp/antigravity" | cut -d ' ' -f 1)" = "$want"
 chmod 0755 "$tmp/antigravity"
 mv -f -- "$tmp/antigravity" "$dir/antigravity"
fi
if [ -d "$tmp/accounts/main" ]; then
 chmod 0700 "$tmp/accounts/main"
 mkdir -p -m 0700 -- "$dir/accounts"
 if [ -e "$dir/accounts/main" ]; then
  [ "$replace" = 1 ] || exit 0
  mv -T -- "$dir/accounts/main" "$tmp/old"
 fi
 mv -T -- "$tmp/accounts/main" "$dir/accounts/main" || { [ ! -e "$tmp/old" ] || mv -T -- "$tmp/old" "$dir/accounts/main"; exit 1; }
fi`

func agyDir(layout box.Layout) string { return filepath.Dir(layout.HostAgyBinary()) }

// AgyState reads what the host has of agy.
func (h *Host) AgyState(ctx context.Context, layout box.Layout) (AgyState, error) {
	out, err := h.run(ctx, "bash", "-c", agyStateScript, "dyna-agy-state", agyDir(layout))
	if err != nil {
		return AgyState{}, errors.New("could not read the agy state of the host")
	}
	var state AgyState
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "dyna-agy-binary":
			state.HasBinary = true
		case line == "dyna-agy-account":
			state.HasAccount = true
		case strings.HasPrefix(line, "dyna-agy-sum "):
			state.BinarySum = strings.TrimPrefix(line, "dyna-agy-sum ")
		case strings.HasPrefix(line, "dyna-agy-arch "):
			state.Arch = strings.TrimPrefix(line, "dyna-agy-arch ")
		}
	}
	return state, nil
}

// PackAgy returns the program and the account folder as an opaque archive, as the host has them. The caller only hands it to UnpackAgy.
func (h *Host) PackAgy(ctx context.Context, layout box.Layout, binary, account bool) ([]byte, error) {
	members := []string{}
	if binary {
		members = append(members, "antigravity")
	}
	if account {
		members = append(members, "accounts/main")
	}
	pack, err := h.run(WithRemoteCancellation(ctx), append([]string{"bash", "-c", agyPackScript, "dyna-agy-pack", agyDir(layout)}, members...)...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not pack the agy files on the source host")
	}
	return pack, nil
}

// UnpackAgy installs an archive from PackAgy under this host's own agy folder. binarySum is the program's checksum on the source, or ""
// when the archive holds no program. replace puts the archive's account folder over one that is there.
func (h *Host) UnpackAgy(ctx context.Context, layout box.Layout, pack []byte, binarySum string, replace bool) error {
	want, swap := binarySum, "0"
	if want == "" {
		want = "-"
	}
	if replace {
		swap = "1"
	}
	if _, err := h.runInput(WithRemoteCancellation(ctx), pack, "bash", "-c", agyUnpackScript, "dyna-agy-unpack", agyDir(layout), want, swap); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not unpack the agy files on the target host")
	}
	return nil
}
