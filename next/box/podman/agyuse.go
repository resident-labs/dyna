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

// Switching the active Antigravity account swaps one file on every agy host: the sign-in, antigravity-acp/antigravity-oauth-token in the
// host's account folder. The token travels as an opaque stream that the engine only carries; nothing here reads, parses, logs or hashes it,
// and every failure is a fixed text, because a failed script's output could otherwise carry it.

const agyTokenRel = "accounts/main/antigravity-acp/antigravity-oauth-token"

const (
	agyInstallExitNone  = 17 // the host has no account folder to put a token in
	agyInstallExitCopy  = 18 // the take-back could not be made, or does not match its source: nothing was changed
	agyInstallExitSize  = 19 // the stream was shorter than the token's size: nothing was changed
	agyInstallExitCheck = 20 // the installed file differs from the stream's second copy
	agyInstallExitKill  = 21 // an agy server on the host survived the kill: nothing was swapped
	agyInstallExitLock  = 22 // the seed's lock was not free in time: nothing was changed
	agyInstallExitLink  = 23 // the token file, or a folder on its path, is a link, or the take-back copy is not a plain file: nothing was changed
)

// ErrAgyTokenMismatch says the installed token file did not compare equal to the stored copy.
var ErrAgyTokenMismatch = errors.New("the token file on the host does not match the stored copy after the swap")

// agyInstallScript swaps the host's token for the stream, under the seed's lock, so a seed and a swap never interleave. $1 is the agy
// folder, $2 where to copy the current token to first ("-" for no take-back), $3 the token's size in bytes, $4 and $5 the agy program and
// account folder as the box's command line names them. stdin holds the new token twice. In order, all under the lock:
//
//  1. The first copy is written beside the old token and its size checked.
//  2. If the host's token already is that copy, nothing else happens ("dyna-agy-same"). This is decided here, under the lock, and not
//     before it: a take-back is only made when the host's token differs from the incoming one, so a run that meets a finished swap never
//     copies the incoming account's live token over the outgoing account's stored copy.
//  3. Every agy server on the host is ended, and the script fails if one is still there afterwards. A process is an agy server only if its
//     whole command line equals the box's, byte for byte (/opt/agy/antigravity acp --gemini-dir /opt/agy/account --app-data-dir
//     antigravity-acp, which a rootless podman shows on the host unchanged), read from /proc and compared with cmp, not searched for with a
//     pattern: so a sign-in's own login (whose folder and --login differ), any other process, and this script's own shell are never touched.
//     The kill command is DYNA_AGY_KILL, "kill" unless a test sets it.
//  4. The take-back: the host's token copied beside its destination and checked against its source with cmp before it is renamed in (a
//     failed one changes nothing). Servers are gone by now, so nothing refreshes the token meanwhile.
//     Every read of the token file is of a plain file: a link on its path (the account folder is writable from an agy box, so an agent can plant
//     one) fails the host before anything is read or changed, the take-back is copied with cp -P and must itself be a plain, non-empty file, and
//     the file is checked again before each comparison. A rename never follows a link, so the install replaces one.
//  5. The new token renamed over the old with mv -T, and the second copy of the stream compared with the installed file.
//
// Dyna reads nothing inside the account folder.
const agyInstallScript = `set -eu
dir=$1 takeback=$2 size=$3 binary=$4 account=$5
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
tmp="" copy=""
trap 'rm -f -- "$tmp" "$copy"' EXIT
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
if [ "$takeback" != - ]; then
 plain "$token" || exit 18
 copy=$(mktemp "$(dirname -- "$takeback")/.copy-XXXXXX")
 cp -P -- "$token" "$copy" || exit 23
 plain "$copy" && [ -s "$copy" ] || exit 23
 cmp -s -- "$token" "$copy" || exit 18
 chmod 0600 -- "$copy"
 mv -T -- "$copy" "$takeback"
 copy=""
fi
echo "dyna-agy-killed $killed"
mv -T -- "$tmp" "$token"
tmp=""
plain "$token" && head -c "$size" | cmp -s - "$token" || exit 20`

// InstallAgyToken swaps the host's sign-in for the token stream r delivers twice, size bytes each time, after copying the current one to
// takeback when that is not "-". It returns how many agy server processes it ended, or same when the host had the token already and
// nothing was done.
func (h *Host) InstallAgyToken(ctx context.Context, layout box.Layout, takeback string, size int64, r io.Reader) (killed int, same bool, err error) {
	var out bytes.Buffer
	err = h.stream(ctx, r, &out, "bash", "-c", agyInstallScript, "dyna-agy-install", agyDir(layout), takeback, strconv.FormatInt(size, 10), box.AgyBinary, contract.AgyGeminiDir)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case agyInstallExitNone:
				return 0, false, errors.New("the host has no agy account folder; seed it first")
			case agyInstallExitCopy:
				return 0, false, errors.New("could not copy the current token back; nothing was changed on the host")
			case agyInstallExitSize:
				return 0, false, errors.New("the token stream was cut short; nothing was changed on the host")
			case agyInstallExitCheck:
				return 0, false, ErrAgyTokenMismatch
			case agyInstallExitKill:
				return 0, false, errors.New("could not end the agy processes on the host; nothing was swapped")
			case agyInstallExitLink:
				return 0, false, errors.New("the token file on the host, or a folder on its path, is a link; nothing was changed")
			case agyInstallExitLock:
				return 0, false, errors.New("the host's agy folder stayed locked by a seed; nothing was changed")
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
