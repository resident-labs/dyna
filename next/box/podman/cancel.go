package podman

import (
	"context"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/box"
)

type remoteCancelKey struct{}
type remoteCancelDeadlineKey struct{}

// WithRemoteCancellation asks the SSH boundary to own a remote process group as well as its local SSH process.
// Cancellation kills and joins that exact group independently before returning to resource cleanup.
func WithRemoteCancellation(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteCancelKey{}, true)
}

// WithRemoteCancellationDeadline reserves a caller's overall cleanup deadline for independent cancellation too.
func WithRemoteCancellationDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(WithRemoteCancellation(ctx), remoteCancelDeadlineKey{}, deadline)
}

type RemoteCancellationUnconfirmed struct{ Err error }

func (e RemoteCancellationUnconfirmed) Error() string {
	return fmt.Sprintf("remote start cancellation unconfirmed: %v", e.Err)
}
func (e RemoteCancellationUnconfirmed) Unwrap() error { return e.Err }

func (h *Host) cancelRemote(ctx context.Context, token string) error {
	args := append(h.direct(), h.destination, "bash -c "+quote(cancelRemoteScript)+" dyna-cancel "+quote(token))
	if _, err := h.runner.Run(ctx, nil, args...); err != nil {
		return RemoteCancellationUnconfirmed{Err: err}
	}
	h.pendingMu.Lock()
	delete(h.pendingCancels, token)
	h.pendingMu.Unlock()
	return nil
}
func (h *Host) joinPending(ctx context.Context) error {
	h.pendingMu.Lock()
	tokens := make([]string, 0, len(h.pendingCancels))
	for token := range h.pendingCancels {
		tokens = append(tokens, token)
	}
	h.pendingMu.Unlock()
	if len(tokens) == 0 {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, box.StartCleanupBudget)
	defer cancel()
	for _, token := range tokens {
		if err := h.cancelRemote(bounded, token); err != nil {
			return err
		}
	}
	return nil
}

// executeOver starts this entire frame under setsid, not merely its setup command: receive, checksum verification,
// output drain and trailer all belong to the recorded group. Registration and the marker check precede blocking I/O.
// A marker without a pid fences an SSH shell that has not launched yet; atomic pid publication closes the other race.
// Paths are NUL-delimited: a layout may contain spaces or newlines. Only paths registered before creation are removed.
const removeFrameTemps = `if [ -f "$dir/temps" ]; then
 while IFS= read -r -d '' temporary; do rm -rf -- "$temporary" || exit 1; done < "$dir/temps"
fi
`

const cancellableFrameScript = `set -u
dir="/tmp/dyna-start-$1"
shift
mkdir -m 700 "$dir" 2>/dev/null || test -d "$dir" || exit 254
printf '%s\n' "$$" > "$dir/pid.tmp" || exit 254
mv -f "$dir/pid.tmp" "$dir/pid" || exit 254
normal=0
trap 'if [ "$normal" -eq 1 ]; then rm -rf -- "$dir"; fi' EXIT
if test -f "$dir/cancel"; then normal=1; exit 130; fi
IFS= read -r header || exit 254
case "$header" in "DYNA-IN "*) size=${header#DYNA-IN } ;; *) exit 254 ;; esac
case "$size" in *[!0-9]*|'') exit 254 ;; esac
[ "${#size}" = 20 ] || exit 254
size=$((10#$size))
head -c "$size" > "$dir/in" || exit 254
[ "$(wc -c < "$dir/in")" -eq "$size" ] || exit 254
if test -f "$dir/cancel"; then normal=1; exit 130; fi
export DYNA_START_DIR="$dir"
"$@" < "$dir/in" > "$dir/out" 2> "$dir/err"
status=$?
` + removeFrameTemps + `if [ "$status" -eq 0 ]; then cat "$dir/err" >&2; else cat "$dir/err" >> "$dir/out"; fi
cat "$dir/out"
printf 'DYNA-END %020d %03d\n' "$(wc -c < "$dir/out")" "$status"
normal=1
`

// /proc, unlike kill -0 of a leader, can confirm every member, including receive/output workers. Zombies cannot do more
// work and may await an unrelated sshd or init's reap. A live unkillable member returns failure, never confirmed success.
const cancelRemoteScript = `set -eu
dir="/tmp/dyna-start-$1"
mkdir -m 700 "$dir" 2>/dev/null || test -d "$dir" || exit 1
touch "$dir/cancel"
if ! test -f "$dir/pid"; then exit 0; fi
read -r pid < "$dir/pid"
case "$pid" in *[!0-9]*|'') exit 1 ;; esac
kill -KILL -- "-$pid" 2>/dev/null || true
end=$((SECONDS+20))
while true; do
 live=0
 for file in /proc/[0-9]*/stat; do
  if IFS= read -r line < "$file" 2>/dev/null; then
   read -r state parent group rest <<< "${line##*) }"
   if [ "$group" = "$pid" ] && [ "$state" != Z ] && [ "$state" != X ]; then live=1; break; fi
  fi
 done
 if [ "$live" -eq 0 ]; then
 ` + removeFrameTemps + `rm -rf -- "$dir"; exit 0
 fi
 if [ "$SECONDS" -ge "$end" ]; then exit 1; fi
 sleep 0.05
done
`
