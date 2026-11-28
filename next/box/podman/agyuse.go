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
)

// ErrAgyTokenMismatch says the installed token file did not compare equal to the stored copy.
var ErrAgyTokenMismatch = errors.New("the token file on the host does not match the stored copy after the swap")

// agyTokenSameScript compares the host's token file with stdin: status 0 for the same bytes, 1 for different ones, 2 for no token file.
const agyTokenSameScript = `set -eu
token=$1/` + agyTokenRel + `
[ -f "$token" ] || exit 2
cmp -s - "$token"`

const agyTokenExitDiffer = 1

// agyInstallScript swaps the host's token for the stream, under the seed's lock, so a seed and a swap never interleave. $1 is the agy
// folder, $2 where to copy the current token to first ("-" for no take-back), $3 the token's size in bytes. stdin holds the new token
// twice. In order: the take-back, made beside its destination and checked against its source with cmp before it is renamed in (a failed
// one changes nothing); the new token written beside the old one and its size checked; every agy server process on the host ended, the
// sign-in's own login processes left alone, and the count printed; the new token renamed over the old with mv -T; and the second copy of
// the stream compared with the installed file. Dyna reads nothing inside the account folder. The pattern given to pgrep is bracketed so
// that this script's own command line does not match it.
const agyInstallScript = `set -eu
dir=$1 takeback=$2 size=$3
token=$dir/` + agyTokenRel + `
folder=$(dirname -- "$token")
mkdir -p -m 0700 -- "$dir"
exec 9> "$dir/.seed.lock"
flock -x 9
[ -d "$folder" ] || exit 17
tmp="" copy=""
trap 'rm -f -- "$tmp" "$copy"' EXIT
if [ "$takeback" != - ]; then
 [ -f "$token" ] || exit 18
 copy=$(mktemp "$(dirname -- "$takeback")/.copy-XXXXXX")
 cp -- "$token" "$copy" || exit 18
 cmp -s -- "$token" "$copy" || exit 18
 chmod 0600 -- "$copy"
fi
tmp=$(mktemp "$folder/.dyna-token.XXXXXX")
head -c "$size" > "$tmp"
[ "$(wc -c < "$tmp")" = "$size" ] || exit 19
chmod 0600 -- "$tmp"
if [ "$takeback" != - ]; then
 mv -T -- "$copy" "$takeback"
 copy=""
fi
killed=0
for pid in $(pgrep -f '[a]ntigravity acp' || true); do
 if ! tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -q -- '--login'; then
  kill -KILL "$pid" 2>/dev/null && killed=$((killed + 1)) || true
 fi
done
echo "dyna-agy-killed $killed"
mv -T -- "$tmp" "$token"
tmp=""
head -c "$size" | cmp -s - "$token" || exit 20`

// AgyTokenIs says whether the host's token file is the same bytes as r delivers: the cmp that decides a host is done already.
func (h *Host) AgyTokenIs(ctx context.Context, layout box.Layout, r io.Reader) (bool, error) {
	err := h.stream(ctx, r, nil, "bash", "-c", agyTokenSameScript, "dyna-agy-token-is", agyDir(layout))
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == agyTokenExitDiffer {
		return false, nil
	}
	return false, errors.New("could not compare the token file on the host")
}

// InstallAgyToken swaps the host's sign-in for the token stream r delivers twice, size bytes each time, after copying the current one to
// takeback when that is not "-". It returns how many agy server processes it ended.
func (h *Host) InstallAgyToken(ctx context.Context, layout box.Layout, takeback string, size int64, r io.Reader) (int, error) {
	var out bytes.Buffer
	err := h.stream(ctx, r, &out, "bash", "-c", agyInstallScript, "dyna-agy-install", agyDir(layout), takeback, strconv.FormatInt(size, 10))
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case agyInstallExitNone:
				return 0, errors.New("the host has no agy account folder; seed it first")
			case agyInstallExitCopy:
				return 0, errors.New("could not copy the current token back; nothing was changed on the host")
			case agyInstallExitSize:
				return 0, errors.New("the token stream was cut short; nothing was changed on the host")
			case agyInstallExitCheck:
				return 0, ErrAgyTokenMismatch
			}
		}
		return 0, errors.New("could not swap the token file on the host")
	}
	killed := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if n, ok := strings.CutPrefix(line, "dyna-agy-killed "); ok {
			killed, _ = strconv.Atoi(n)
		}
	}
	return killed, nil
}
