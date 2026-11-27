package podman

import (
	"context"
	"errors"
	"io"
	"os/exec"
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
if [ -e "$1/accounts/main" ] || [ -L "$1/accounts/main" ]; then echo dyna-agy-account; fi
echo "dyna-agy-arch $(uname -s -m)"
`

// agyPackScript writes a tar of the members named after $1 (paths below $1) to stdout, leaving out by path what agy writes in a running
// session: the conversations and their per-conversation brain (nearly all of an account folder's size), the logs, and the annotations (per
// session artifacts), and what changes every few seconds in a live session: the live SQLite index of the conversations
// (conversation_summaries.db with its -shm and -wal, jetbox_summaries_proto.pb) and bin/agentapi. The sign-in, the config and the mcp, cache and implicit state stay. A file that changes while tar reads it (tar
// status 1) ends the pack with status 19 and not as a good archive. Then a second tar holds only an empty .dyna-complete: an archive
// without it was cut short, which tar alone cannot tell at a member boundary. Its stderr is dropped so that only the archive is on stdout. Nothing of the archive is written to a file.
const agyPackScript = `set -eu
cd -- "$1"
shift
acp=accounts/main/antigravity-acp
excludes=""
for d in brain conversations log annotations conversation_summaries.db conversation_summaries.db-shm conversation_summaries.db-wal jetbox_summaries_proto.pb; do excludes="$excludes --exclude=$acp/$d"; done
excludes="$excludes --exclude=accounts/main/antigravity-acp/bin/agentapi"
marker=$(mktemp)
trap 'rm -f -- "$marker"' EXIT
st=0
tar -c -f - $excludes -- "$@" 2>/dev/null || st=$?
if [ "$st" = 1 ]; then exit 19; fi
if [ "$st" != 0 ]; then exit "$st"; fi
tar -c -f - --transform='s,.*,.dyna-complete,' -- "$marker" 2>/dev/null`

// agyPackExitChanged is the pack script's exit status for a file that changed while it was read: the archive is cut, so the target takes
// nothing, and a retry is safe.
const agyPackExitChanged = 19

// ErrAgyChanged says a running agy session changed a file while it was packed.
var ErrAgyChanged = errors.New("a running agy session changed a file while the account was packed; retry, or close the agy sessions on the source host first")

// agyUnpackExitSeeded is the unpack script's exit status for a host that has an account folder: nothing was changed.
const agyUnpackExitSeeded = 17

// ErrAgySeeded says the host has an account folder, so the seed copied nothing. It is a fact about the host, not a fault.
var ErrAgySeeded = errors.New("the host has an agy account folder already")

// agyUnpackExitProgram is its exit status for a program path that holds something that is not an executable file, or that is missing and
// was not sent: nothing was published, the account folder included.
const agyUnpackExitProgram = 18

// ErrAgyProgram says the host's agy program path cannot be used as it is, so nothing was published.
var ErrAgyProgram = errors.New("the agy program on the host is not an executable file")

// agyUnpackScript extracts the tar on stdin under a temporary folder in the agy folder $1, on the folder's own filesystem and never under
// /tmp, and publishes it: the program first, only if there is none, after its checksum $2 (or "-" for none) matches, and the account
// folder last, by one no-clobber rename of the complete folder. A program path that holds anything but an executable file (a dangling
// link too) ends it with status 18 before anything is extracted, and an existing executable one is kept, whatever the source's is. All of it, the sweep of an earlier seed's temporary folder included, runs
// under one flock, so two seeds never interleave. An existing accounts/main, a dangling link at that path too, ends it with status 17
// before anything is extracted and again just before the rename; the rename's result is checked by the folder's identity (a stat, never a
// read of its contents), because mv -n succeeds when it declines. The temporary folder is 0700 and goes on every way out, even a signal;
// one left by a kill is swept by the next seed. A kill after the program is published leaves the program and no account folder, which a
// later seed completes. Dyna reads nothing inside the account folder.
const agyUnpackScript = `set -eu
dir=$1 want=$2
main=$dir/accounts/main
seeded() { [ -e "$main" ] || [ -L "$main" ]; }
mkdir -p -m 0700 -- "$dir"
exec 9> "$dir/.seed.lock"
flock -x 9
rm -rf -- "$dir"/.seed.??????
if seeded; then exit 17; fi
bin=$dir/antigravity
present() { [ -e "$bin" ] || [ -L "$bin" ]; }
usable() { [ -f "$bin" ] && [ -x "$bin" ]; }
if present && ! usable; then exit 18; fi
tmp=$(mktemp -d "$dir/.seed.XXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
tar -x -i -p --no-same-owner -C "$tmp" -f -
[ -f "$tmp/.dyna-complete" ]
[ -d "$tmp/accounts/main" ]
if ! present; then
 [ "$want" != - ] || exit 18
 test "$(sha256sum < "$tmp/antigravity" | cut -d ' ' -f 1)" = "$want"
 chmod 0755 "$tmp/antigravity"
 program=$(stat -c %d:%i -- "$tmp/antigravity")
 mv -T -n -- "$tmp/antigravity" "$bin"
 if [ "$(stat -c %d:%i -- "$bin" 2>/dev/null)" != "$program" ]; then usable || exit 18; fi
fi
usable || exit 18
chmod 0700 "$tmp/accounts/main"
mkdir -p -m 0700 -- "$dir/accounts"
identity=$(stat -c %d:%i -- "$tmp/accounts/main")
if seeded; then exit 17; fi
mv -T -n -- "$tmp/accounts/main" "$main"
[ "$(stat -c %d:%i -- "$main")" = "$identity" ] || exit 17`

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
	// Packing the account first only stops the program delaying the account read; it does not shorten tar's per-file changed-while-reading window.
	members := []string{}
	if account {
		members = append(members, "accounts/main")
	}
	if binary {
		members = append(members, "antigravity")
	}
	if err := h.stream(ctx, nil, w, append([]string{"bash", "-c", agyPackScript, "dyna-agy-pack", agyDir(layout)}, members...)...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == agyPackExitChanged {
			return ErrAgyChanged
		}
		return errors.New("could not pack the agy files on the source host")
	}
	return nil
}

// UnpackAgy installs the archive from PackAgy that r delivers under this host's own agy folder, and says ErrAgySeeded when the host has an
// account folder already, and ErrAgyProgram when its program path is unusable. binarySum is the program's checksum on the source, or "" when the archive holds no program.
func (h *Host) UnpackAgy(ctx context.Context, layout box.Layout, r io.Reader, binarySum string) error {
	want := binarySum
	if want == "" {
		want = "-"
	}
	if err := h.stream(ctx, r, nil, "bash", "-c", agyUnpackScript, "dyna-agy-unpack", agyDir(layout), want); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == agyUnpackExitSeeded {
			return ErrAgySeeded
		}
		if errors.As(err, &exit) && exit.ExitCode() == agyUnpackExitProgram {
			return ErrAgyProgram
		}
		return errors.New("could not unpack the agy files on the target host")
	}
	return nil
}
