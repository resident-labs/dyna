package podman

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Switching the active Antigravity account installs one file on every agy host: the account's stored sign-in, as signed in, becomes
// antigravity-acp/antigravity-oauth-token in the host's account folder. The token travels as an opaque stream that the engine only carries;
// nothing here reads, parses, logs or hashes it, and every failure is a fixed text, because a failed script's output could otherwise carry
// it. The live file is never copied back out: accounts/main is a folder boxes write, so nothing is read from it to be kept or published. The
// one read is the comparison below, which only decides whether the host is skipped.

const agyTokenRel = "accounts/main/antigravity-acp/antigravity-oauth-token"

const (
	agyInstallExitNone  = 17 // the host has no account folder to put a token in
	agyInstallExitSize  = 19 // the stream was shorter than the token's size: nothing was changed
	agyInstallExitCheck = 20 // the installed file differs from the stream's second copy
	agyInstallExitKill  = 21 // an agy server on the host survived the kill: nothing was swapped
	agyInstallExitLock  = 22 // the seed's lock was not free in time: nothing was changed
	agyInstallExitLink  = 23 // the token file, or a folder on its path, is a link, or is not where it belongs: nothing was changed
)

// ErrAgyTokenMismatch says the installed token file did not compare equal to the stored copy.
var ErrAgyTokenMismatch = errors.New("the token file on the host does not match the stored copy after the swap")

// agyInstallScript installs the stream as the host's token, under the seed's lock, so a seed and a swap never interleave. $1 is the agy
// folder, $2 the token's size in bytes, $3 and $4 the agy program and account folder as the box's command line names them. stdin holds the
// new token twice. In order, all under the lock:
//
//  1. No link is on the token's path (accounts, accounts/main, antigravity-acp and the token itself): the account folder is writable from an
//     agy box, so an agent can plant one. A link fails the host before anything is read or changed.
//  2. The first copy is written beside the old token and its size checked.
//  3. If the host's token already is that copy, nothing else happens ("dyna-agy-same"): the comparison only decides whether to skip the
//     host, and nothing it reads is kept or sent anywhere. It is made here, under the lock, and not before it.
//  4. Every agy server on the host is ended, and the script fails if one is still there afterwards. A process is an agy server only if its
//     whole command line equals the box's, byte for byte (/opt/agy/antigravity acp --gemini-dir /opt/agy/account --app-data-dir
//     antigravity-acp, which a rootless podman shows on the host unchanged), read from /proc and compared with cmp, not searched for with a
//     pattern: so a sign-in's own login (whose folder and --login differ), any other process, and this script's own shell are never touched.
//     The kill command is DYNA_AGY_KILL, "kill" unless a test sets it. One Dyna installation per host is assumed, as the shared host paths do.
//  5. The account folder's resolved path must be the expected one under the agy folder, with no link in it, and then the new token is
//     renamed over the old with mv -T (a rename never follows a link) and the second copy of the stream compared with the installed file. A
//     box that swaps the folder between that check and the rename is accepted: it would have to do so on purpose during the switch, and the
//     worst result is the token landing in a wrong file, never another file's bytes being published.
const agyInstallScript = `set -eu
dir=$1 size=$2 binary=$3 account=$4
token=$dir/` + agyTokenRel + `
folder=$(dirname -- "$token")
mkdir -p -m 0700 -- "$dir"
exec 9> "$dir/.seed.lock"
flock -x -w 100 9 || exit 22
for p in "$dir/accounts" "$dir/accounts/main" "$folder" "$token"; do
 [ ! -L "$p" ] || exit 23
done
[ -d "$folder" ] || exit 17
plain() { [ -f "$1" ] && [ ! -L "$1" ]; }
tmp=""
trap 'rm -f -- "$tmp"' EXIT
tmp=$(mktemp "$folder/.dyna-token.XXXXXX")
head -c "$size" > "$tmp"
[ "$(wc -c < "$tmp")" = "$size" ] || exit 19
chmod 0600 -- "$tmp"
if plain "$token" && cmp -s -- "$token" "$tmp"; then
 cat > /dev/null
 echo "dyna-agy-same"
 exit 0
fi
agy_pids() {
 for proc in /proc/[0-9]*; do
  if printf '%s\0' "$binary" acp --gemini-dir "$account" --app-data-dir antigravity-acp | cmp -s - "$proc/cmdline" 2>/dev/null; then
   echo "${proc#/proc/}"
  fi
 done
}
killed=0
for pid in $(agy_pids); do
 ${DYNA_AGY_KILL:-kill} -KILL "$pid" 2>/dev/null && killed=$((killed + 1)) || true
done
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
 [ -z "$(agy_pids)" ] && break
 sleep 0.1
done
[ -z "$(agy_pids)" ] || exit 21
want=$(cd "$dir" && pwd -P)/accounts/main/antigravity-acp
[ "$(realpath -- "$folder")" = "$want" ] || exit 23
echo "dyna-agy-killed $killed"
mv -T -- "$tmp" "$token"
tmp=""
plain "$token" && head -c "$size" | cmp -s - "$token" || exit 20`

// InstallAgyToken installs the token stream r delivers twice, size bytes each time, as the host's sign-in. It returns how many agy server
// processes it ended, or same when the host had the token already and nothing was done.
func (h *Host) InstallAgyToken(ctx context.Context, layout box.Layout, size int64, r io.Reader) (killed int, same bool, err error) {
	var out bytes.Buffer
	err = h.stream(ctx, r, &out, "bash", "-c", agyInstallScript, "dyna-agy-install", agyDir(layout), strconv.FormatInt(size, 10), box.AgyBinary, contract.AgyGeminiDir)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case agyInstallExitNone:
				return 0, false, errors.New("the host has no agy account folder; seed it first")
			case agyInstallExitSize:
				return 0, false, errors.New("the token stream was cut short; nothing was changed on the host")
			case agyInstallExitCheck:
				return 0, false, ErrAgyTokenMismatch
			case agyInstallExitKill:
				return 0, false, errors.New("could not end the agy processes on the host; nothing was swapped")
			case agyInstallExitLock:
				return 0, false, errors.New("the host's agy folder stayed locked by a seed; nothing was changed")
			case agyInstallExitLink:
				return 0, false, errors.New("the token file on the host, or a folder on its path, is a link or not where it belongs; nothing was changed")
			}
		}
		return 0, false, errors.New("could not swap the token file on the host")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if line == "dyna-agy-same" {
			return 0, true, nil
		}
		if n, ok := strings.CutPrefix(line, "dyna-agy-killed "); ok {
			killed, _ = strconv.Atoi(n)
		}
	}
	return killed, false, nil
}
