package podman

import "context"

type remoteCancelKey struct{}

// WithRemoteCancellation asks the SSH boundary to own a remote process group as well as its local SSH process.
// Cancellation kills that exact group over an independent request before returning to resource cleanup.
func WithRemoteCancellation(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteCancelKey{}, true)
}

// A cancellation may arrive before SSH starts its remote shell. The cancel marker fences that late launch. Once launched,
// the child waits for go, published only after its process group is recorded and cancellation checked.
const cancellableFrameScript = `set -u
dir="/tmp/dyna-start-$1"
shift
mkdir -m 700 "$dir" 2>/dev/null || test -d "$dir" || exit 254
trap 'if [ -n "${pid:-}" ]; then kill -KILL -- "-$pid" 2>/dev/null || true; fi; rm -rf -- "$dir"' EXIT HUP INT TERM
IFS= read -r header || exit 254
case "$header" in "DYNA-IN "*) size=${header#DYNA-IN } ;; *) exit 254 ;; esac
case "$size" in *[!0-9]*|'') exit 254 ;; esac
[ "${#size}" = 20 ] || exit 254
size=$((10#$size))
head -c "$size" > "$dir/in" || exit 254
[ "$(wc -c < "$dir/in")" -eq "$size" ] || exit 254
setsid bash -c 'dir=$1; shift; while ! test -f "$dir/go"; do test ! -f "$dir/cancel" || exit 130; sleep 0.02; done; exec "$@"' dyna-child "$dir" "$@" < "$dir/in" > "$dir/out" 2> "$dir/err" &
pid=$!
printf '%s\n' "$pid" > "$dir/pid"
if test -f "$dir/cancel"; then kill -KILL -- "-$pid" 2>/dev/null || true; else touch "$dir/go"; fi
wait "$pid"
status=$?
if [ "$status" -eq 0 ]; then cat "$dir/err" >&2; else cat "$dir/err" >> "$dir/out"; fi
cat "$dir/out"
printf 'DYNA-END %020d %03d\n' "$(wc -c < "$dir/out")" "$status"
`

const cancelRemoteScript = `set -eu
dir="/tmp/dyna-start-$1"
mkdir -m 700 "$dir" 2>/dev/null || test -d "$dir" || exit 1
touch "$dir/cancel"
if test -f "$dir/pid"; then
 read -r pid < "$dir/pid"
 kill -KILL -- "-$pid" 2>/dev/null || true
fi
`
