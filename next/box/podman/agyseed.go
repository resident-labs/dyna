package podman

import (
	"context"
	"errors"
	"io"
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
// logs by path, and then a second tar that holds only an empty .dyna-complete: an archive without it was cut short, which tar alone
// cannot tell at a member boundary. Its stderr is dropped so that only the archive is on stdout. Nothing of the archive is written to a file.
const agyPackScript = `set -eu
cd -- "$1"
shift
marker=$(mktemp)
trap 'rm -f -- "$marker"' EXIT
tar -c -f - --exclude=accounts/main/antigravity-acp/conversations --exclude=accounts/main/antigravity-acp/log -- "$@" 2>/dev/null
tar -c -f - --transform='s,.*,.dyna-complete,' -- "$marker" 2>/dev/null`

// agyUnpackScript extracts the tar on stdin under a temporary folder in the agy folder $1, on the folder's own filesystem and never under /tmp,
// requires the complete marker, checks the program's checksum $2 (or "-" for none), and moves each piece into place, so a half copy is
// never at the final path. The temporary folder is 0700 and goes on every way out, even a signal; one left by a kill is swept at the next
// seed, under the lock. The account folder is 0700. A present account folder is left alone unless $3 is 1, and is then put back if the
// move fails. The program is published only if the path is still free once the lock is held, or when $3 is 1.
const agyUnpackScript = `set -eu
dir=$1 want=$2 replace=$3
mkdir -p -m 0700 -- "$dir"
exec 9> "$dir/.seed.lock"
flock -x 9
rm -rf -- "$dir"/.seed.??????
tmp=$(mktemp -d "$dir/.seed.XXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
tar -x -i -p --no-same-owner -C "$tmp" -f -
[ -f "$tmp/.dyna-complete" ]
if [ "$want" != - ]; then
 test "$(sha256sum < "$tmp/antigravity" | cut -d ' ' -f 1)" = "$want"
 chmod 0755 "$tmp/antigravity"
 if [ "$replace" = 1 ]; then
  mv -f -T -- "$tmp/antigravity" "$dir/antigravity"
 elif [ ! -e "$dir/antigravity" ]; then
  mv -T -- "$tmp/antigravity" "$dir/antigravity"
 fi
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

// stream runs a command on the host with its input and output as streams, over the host's own connection and under its gates. A driver that
// cannot stream refuses: the archive is never spooled to a file on either host to make up for it.
func (h *Host) stream(ctx context.Context, in io.Reader, out io.Writer, argv ...string) error {
	streamer, ok := h.runner.(StreamRunner)
	if !ok {
		return errors.New("this host's driver cannot stream")
	}
	select {
	case h.shortGate <- struct{}{}:
		defer func() { <-h.shortGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := h.lock(ctx); err != nil {
		return err
	}
	err := h.ensure(ctx)
	h.unlock()
	if err != nil {
		return err
	}
	if err := h.joinPending(ctx); err != nil {
		return err
	}
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = quote(arg)
	}
	return streamer.RunStream(ctx, in, out, append(h.base(), h.destination, strings.Join(parts, " "))...)
}

// PackAgy writes the program and the account folder as an opaque archive to w, as the host has them. The caller only hands it on to
// UnpackAgy, through a pipe, so the engine holds no more of it than the pipe's one write at a time.
func (h *Host) PackAgy(ctx context.Context, layout box.Layout, binary, account bool, w io.Writer) error {
	members := []string{}
	if binary {
		members = append(members, "antigravity")
	}
	if account {
		members = append(members, "accounts/main")
	}
	if err := h.stream(ctx, nil, w, append([]string{"bash", "-c", agyPackScript, "dyna-agy-pack", agyDir(layout)}, members...)...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not pack the agy files on the source host")
	}
	return nil
}

// UnpackAgy installs the archive from PackAgy that r delivers under this host's own agy folder. binarySum is the program's checksum on
// the source, or "" when the archive holds no program. replace puts the archive's account folder over one that is there.
func (h *Host) UnpackAgy(ctx context.Context, layout box.Layout, r io.Reader, binarySum string, replace bool) error {
	want, swap := binarySum, "0"
	if want == "" {
		want = "-"
	}
	if replace {
		swap = "1"
	}
	if err := h.stream(ctx, r, nil, "bash", "-c", agyUnpackScript, "dyna-agy-unpack", agyDir(layout), want, swap); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not unpack the agy files on the target host")
	}
	return nil
}
