package podman

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// NamespaceSuperseded means a newer creation owns these names. The old deleter
// is complete, not retryable; it must not acquire the new epoch and try again.
type NamespaceSuperseded = box.NamespaceSuperseded

// NamespaceUncertain retains ownership when the bounded host lock or epoch
// cannot be read. It does not mean that the namespace is absent.
type NamespaceUncertain struct {
	Box contract.BoxID
	Err error
}

func (e NamespaceUncertain) Error() string {
	return fmt.Sprintf("box %s: host namespace uncertain: %v", e.Box, e.Err)
}
func (e NamespaceUncertain) Unwrap() error { return e.Err }

// namespaceProcAccess is shared with fixture tests of the procfs trust boundary.
const namespaceProcAccess = `namespace_other_user() {
 while read -r field real effective rest; do
  if test "$field" = Uid:; then
   test "$real" != "$EUID" && test "$effective" != "$EUID"
   return $?
  fi
 done < "$1/status" 2>/dev/null
 return 1
}
namespace_unreadable_safe() {
 namespace_other_user "$1" && return 0
 # Session title names are a correctness guard, not a security boundary:
 # a malicious same-user process could forge them, but wrapper descendants
 # never carry these names. Check argv[0], not arbitrary command arguments.
 command=
 IFS= read -r -d '' command < "$1/cmdline" 2>/dev/null || true
 case ${command##*/} in
  sshd-session:*|sshd:*|'(sd-pam)'|ssh-agent|gpg-agent) return 0;;
 esac
 # age_ticks is accepted only from same-boot metadata bound to this inode.
 # Only wrappers open the file; older unrelated processes cannot inherit it.
 # Start ticks round down, while our pre-creation sample rounds up: exclude
 # the ambiguous boundary tick too. Realtime adjustments never enter this proof.
 test "$age_ticks" -gt 0 || return 1
 IFS= read -r process_stat < "$1/stat" || return 1
 read -ra fields <<< "${process_stat##*) }"
 start=${fields[19]:-}
 [[ "$start" =~ ^[0-9]+$ ]] || return 1
 test $((start + 1)) -lt "$age_ticks"
}
namespace_birth_clock() {
 IFS= read -r birth_boot < /proc/sys/kernel/random/boot_id || return 1
 read -r uptime rest < /proc/uptime || return 1
 hz=$(getconf CLK_TCK) || return 1
 [[ "$uptime" =~ ^([0-9]+)\.([0-9]{2})$ ]] || return 1
 # Linux exports uptime in hundredths. Sample before creating the inode.
 birth_ticks=$((BASH_REMATCH[1] * hz + (10#${BASH_REMATCH[2]} * hz + 99) / 100))
}
namespace_write_birth() {
 inode=$(stat -c %d:%i -- "$1") || return 1
 printf '%s %s %s\n' "$birth_boot" "$birth_ticks" "$inode" > "$birth_file.new" || return 1
 mv -f -- "$birth_file.new" "$birth_file"
}
namespace_load_birth() {
 age_ticks=0
 IFS= read -r current_boot < /proc/sys/kernel/random/boot_id || return 1
 read -r recorded_boot recorded_ticks recorded_inode extra 2>/dev/null < "$birth_file" || return 1
 test -z "$extra" && test "$recorded_boot" = "$current_boot" || return 1
 [[ "$recorded_ticks" =~ ^[1-9][0-9]*$ ]] && test "${#recorded_ticks}" -lt 18 || return 1
 inode=$(stat -c %d:%i -- "$root/locks/box-$id.lock") || return 1
 test "$recorded_inode" = "$inode" || return 1
 age_ticks=$recorded_ticks
}
`

// FD 8 is intentionally inherited by every mutating child. In particular, do
// not use flock --close or close this descriptor at a nested exec boundary.
// A completed command explicitly unlocks the shared description: Podman can
// leave conmon holding FD 8 after its synchronous command returns. Without
// this unlock, that daemon fences the box for its entire lifetime. Do not use
// an EXIT unlock trap: interrupted wrappers retain the lock in surviving work.
// All namespace files live outside container, workspace, stack and room trees.
const namespaceScript = namespaceProcAccess + `set -eu
umask 077
trap : HUP PIPE TERM
root=$1; id=$2; mode=$3; shift 3
check=0
if test "$mode" = recover && test "${1:-}" = --check; then check=1; fi
birth_file="$root/locks/box-$id.born"
SECONDS=0
if test "$check" = 0; then
 mkdir -p -- "$root/locks"
 exec 9> "$root/locks/box-$id.rotate"
 flock -x -w 15 9 || exit 252
 if ! test -e "$root/locks/box-$id.lock"; then
  namespace_birth_clock || exit 251
  exec 8> "$root/locks/box-$id.lock"
  namespace_write_birth "$root/locks/box-$id.lock" || exit 251
 else
  exec 8> "$root/locks/box-$id.lock"
 fi
 chmod 600 -- "$root/locks/box-$id.lock"
 exec 9>&-
fi
namespace_lock_holders() {
 # The scan runs in a subshell that closes the lock descriptor and starts no program: only builtins read /proc, so killing
 # the subshell leaves nothing behind, and nothing of it can hold the lock or the caller's streams.
 exec 8>&-
 set +e
 printf 'namespace lock timed out: box %s; observed holders:' "$id" >&2
 found=0
 # Inspect only descriptors for this inode with a granted flock. Merely
 # opening the file (including our own FD 8) does not identify a holder.
 # Never signal: a conmon may still own a running container, and probing
 # Podman cannot make a subsequent kill atomic with that container's life.
 for proc in /proc/[0-9]*; do
  for fd in "$proc"/fd/*; do
   test "$fd" -ef "$root/locks/box-$id.lock" || continue
   held=0
   { while IFS= read -r line; do
     case $line in 'lock:'*' FLOCK '*' WRITE '*) held=1;; esac
    done < "$proc/fdinfo/${fd##*/}"; } 2>/dev/null
   test "$held" = 1 || continue
   command=
   { while IFS= read -r -d '' word || test -n "$word"; do command="$command $word"; word=; done < "$proc/cmdline"; } 2>/dev/null
   printf '\n pid=%s command=%.256s' "${proc##*/}" "$command" >&2
   found=1
   break
  done
 done
 test "$found" = 1 || printf ' unavailable (process exited or procfs access denied)' >&2
 printf '\n' >&2
}
# The scan reads every process and descriptor of the host, so it is best-effort and is killed after 2 s. The failure stays exit 252.
namespace_scan_holders() {
 (namespace_lock_holders) &
 scan=$!
 SECONDS=0
 while kill -0 "$scan" 2>/dev/null && test "$SECONDS" -lt 2; do sleep 0.1; done
 if kill -KILL "$scan" 2>/dev/null; then printf ' (scan stopped after 2s)\n' >&2; fi
 wait "$scan" 2>/dev/null || true
}
# Recovery takes only the rotation lock, never the abandoned lock itself.
# Only the host user opens these 0600 files in its private data directory; root
# does not touch them. Inherited descriptors also stay in that user's wrapper
# tree. Unreadable other-user directories cannot matter. For same-user processes
# we require proof they predate the file or a known non-Dyna session title (see
# namespace_unreadable_safe). All other unreadable processes refuse rotation.
namespace_stale_holders() {
 exec 8>&- 9>&-
 pids=
 for proc in /proc/[0-9]*; do
  if ! test -r "$proc/fd" || ! test -x "$proc/fd"; then
   test ! -d "$proc" && continue
   namespace_unreadable_safe "$proc" && continue
   printf 'namespace recovery refused: unreadable descriptors for %s\n' "$proc" >&2
   return 1
  fi
  for fd in "$proc"/fd/*; do
   test "$fd" -ef "$root/locks/box-$id.lock" || continue
   held=0
   { while IFS= read -r line; do
     case $line in 'lock:'*' FLOCK '*' WRITE '*) held=1;; esac
    done < "$proc/fdinfo/${fd##*/}"; } 2>/dev/null || {
    printf 'namespace recovery refused: unreadable lock info for %s\n' "$fd" >&2
    return 1
   }
   test "$held" = 1 || continue
   exe=$(readlink -- "$proc/exe") || {
    printf 'namespace recovery refused: unreadable executable for %s\n' "$proc" >&2
    return 1
   }
   test "${exe##*/}" = conmon || {
    printf 'namespace recovery refused: non-conmon holder %s (%s)\n' "$proc" "$exe" >&2
    return 1
   }
   pids="$pids ${proc##*/}"
   break
  done
 done
 test -n "$pids" || test "$check" = 1 || return 1
 printf '%s\n' "$pids"
}
namespace_stale_scan() {
 (namespace_stale_holders) > "$scan_result" &
 scan=$!
 while kill -0 "$scan" 2>/dev/null && test "$SECONDS" -lt 10; do sleep 0.1; done
 if kill -KILL "$scan" 2>/dev/null; then
  wait "$scan" 2>/dev/null || true
  return 1
 fi
 wait "$scan"
}
if test "$mode" = recover; then
 if test "$check" = 1; then
  # --check never creates or modifies anything in the locks directory. Its
  # only scratch is a temporary scan result, removed on exit. No lock FD is open.
  scan_result=$(mktemp "${TMPDIR:-/tmp}/dyna-namespace-check.XXXXXX") || exit 254
  trap 'rm -f -- "$scan_result"' EXIT
 else
  exec 9> "$root/locks/box-$id.rotate"
  flock -x -w 10 9 || exit 254
  scan_result="$root/locks/box-$id.rotate.new"
 fi
 namespace_load_birth || true
 SECONDS=0
 namespace_stale_scan || exit 254
 IFS= read -r first < "$scan_result" || exit 254
 sleep 1
 namespace_stale_scan || exit 254
 IFS= read -r second < "$scan_result" || exit 254
 test "$first" = "$second" || exit 254
 if test "$check" = 1; then
  printf 'namespace recovery check passed: box %s; eligible conmon pids:%s\n' "$id" "$first"
  exit 0
 fi
 namespace_birth_clock || exit 254
 fresh=$(mktemp "$root/locks/box-$id.lock.XXXXXX") || exit 254
 mv -f -- "$fresh" "$root/locks/box-$id.lock"
 namespace_write_birth "$root/locks/box-$id.lock" || exit 254
 exec 9>&-
 printf 'rotated namespace lock: box %s; conmon pids:%s\n' "$id" "$first"
 exit 0
fi
while :; do
 remaining=$((15 - SECONDS))
 test "$remaining" -gt 0 || { namespace_scan_holders || true; exit 252; }
 flock -x -w "$remaining" 8 || { namespace_scan_holders || true; exit 252; }
 # Fence the check-to-command boundary against rotation. A conmon can exit
 # after the last scan, letting a waiter acquire the old inode before mv.
 exec 9> "$root/locks/box-$id.rotate"
 remaining=$((15 - SECONDS))
 test "$remaining" -gt 0 && flock -x -w "$remaining" 9 || exit 252
 if test /proc/$$/fd/8 -ef "$root/locks/box-$id.lock"; then
  exec 9>&-
  break
 fi
 exec 9>&-
 exec 8> "$root/locks/box-$id.lock"
done
epoch_file="$root/locks/box-$id.epoch"
epoch=0
if test -e "$epoch_file"; then
 IFS= read -r epoch < "$epoch_file" || exit 251
 [[ "$epoch" =~ ^[0-9]+$ ]] || exit 251
 test "${#epoch}" -lt 18 || exit 251
fi
case "$mode" in
 read) printf '%s\n' "$epoch"; exit 0;;
 create) epoch=$((epoch + 1)); printf '%s\n' "$epoch" > "$epoch_file";;
 delete)
 test "$1" = --expect-epoch || exit 251
 expected=$2; shift 2
 test "$epoch" = "$expected" || exit 250;;
 use) epoch=$((epoch + 1)); printf '%s\n' "$epoch" > "$epoch_file";;
 *) exit 251;;
esac
status=0
"$@" || status=$?
flock -u 8
# Reserve 252 for proof that the body never ran. A body using that code is
# still a failure, but must never trigger automatic mutation retry.
test "$status" != 252 || status=1
exit "$status"`

func (h *Host) namespace(ctx context.Context, id contract.BoxID, mode string, command ...string) ([]byte, error) {
	args := []string{"bash", "-c", namespaceScript, "dyna-box-namespace", h.dataDir, string(id), mode}
	run := func(args []string) ([]byte, error) {
		if mode == "read" || mode == "delete" {
			return h.executeOver(ctx, h.direct(), nil, args...)
		}
		return h.run(ctx, args...)
	}
	if mode == "read" || mode == "delete" {
		// Restoring the mux's forwards can unlink a box socket. Read/delete must
		// reach the host epoch check without that implicit mutation as well.
		select {
		case h.shortGate <- struct{}{}:
			defer func() { <-h.shortGate }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out, err := run(append(args, command...))
	var timedOut remoteExit
	if errors.As(err, &timedOut) && timedOut == 252 {
		recovery := []string{"bash", "-c", namespaceScript, "dyna-box-namespace", h.dataDir, string(id), "recover"}
		if recovered, recoveryErr := run(recovery); recoveryErr == nil {
			log.Printf("%s", strings.TrimSpace(string(recovered)))
			out, err = run(append(args, command...))
		}
	}
	var exit remoteExit
	if errors.As(err, &exit) {
		switch exit {
		case 250:
			return out, NamespaceSuperseded{Box: id}
		case 251, 252:
			return out, NamespaceUncertain{Box: id, Err: err}
		}
	}
	return out, err
}

func (h *Host) namespaceDelete(ctx context.Context, id contract.BoxID, args ...string) ([]byte, error) {
	epoch, err := h.BoxEpoch(ctx, id)
	if err != nil {
		return nil, err
	}
	command := append([]string{"--expect-epoch", strconv.FormatUint(epoch, 10)}, args...)
	return h.namespace(ctx, id, "delete", command...)
}

// BoxEpoch captures the deletion decision's host epoch, never engine state.
func (h *Host) BoxEpoch(ctx context.Context, id contract.BoxID) (uint64, error) {
	out, err := h.namespace(ctx, id, "read")
	if err != nil {
		return 0, NamespaceUncertain{Box: id, Err: err}
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, NamespaceUncertain{Box: id, Err: err}
	}
	return n, nil
}

func shellCommand(args ...string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = quote(arg)
	}
	return strings.Join(parts, " ")
}

// CreateBox advances the epoch before ANY mutation and holds one inherited lock
// across recreate's old-container removal, stack creation and main creation.
func (h *Host) CreateBox(ctx context.Context, c box.ContainerSpec, stack *box.Stack, recreate bool) error {
	args, err := h.containerCommand(ctx, c)
	if err != nil {
		return err
	}
	script := "set -eu\n"
	if recreate {
		script += removeContainersCommand(c.Box) + "\n"
	}
	if stack != nil {
		script += "(set -eu\n" + stackCommands(c.Box, c.Slice, stack) + "\n) || exit 253\n"
	}
	script += shellCommand(recoverCreateCommand(c.Box, args)...)
	out, err := h.namespace(ctx, c.Box, "create", "bash", "-c", script, "dyna-create-box", string(c.Box))
	var exit remoteExit
	if errors.As(err, &exit) && exit == 253 {
		return box.StackError{Reason: err.Error()}
	}
	return containerFailure(out, err)
}

func removeContainersCommand(id contract.BoxID) string {
	return strings.Join([]string{
		recoverRemoving,
		"if " + shellCommand("podman", "--cgroup-manager=systemd", "stop", "--ignore", string(id)) + "; then\n" +
			shellCommand("podman", "--cgroup-manager=systemd", "rm", "--ignore", "--volumes", string(id)) + " || { status=$?; test \"$status\" = 125 && recover_removing " + quote(string(id)) + " || exit \"$status\"; }\n" +
			"else status=$?; test \"$status\" = 125 && recover_removing " + quote(string(id)) + " || exit \"$status\"; fi",
		shellCommand("podman", "--cgroup-manager=systemd", "pod", "rm", "--force", "--ignore", "--time", "0", box.PodName(id)),
	}, "\n")
}
func removeVolumesCommand(id contract.BoxID) string {
	return shellCommand("podman", "--cgroup-manager=systemd", "volume", "rm", "dyna-box-"+string(id)) + ` || { status=$?; test "$status" = 1 || exit "$status"; }` + "\n" + shellCommand("podman", "volume", "prune", "--force", "--filter", "label=dyna.stack="+string(id))
}

// StopBox joins runnable work before the fresh Store deletion proof. The later
// destructive body repeats this stop under its own whole-command lock, carrying
// the original epoch throughout; no delete or create is split across locks.
func (h *Host) StopBox(ctx context.Context, id contract.BoxID, epoch uint64) error {
	script := "set -eu\n" + shellCommand("podman", "--cgroup-manager=systemd", "stop", "--ignore", string(id)) + "\n" + shellCommand("podman", "--cgroup-manager=systemd", "pod", "stop", "--ignore", box.PodName(id))
	_, err := h.namespace(ctx, id, "delete", "--expect-epoch", strconv.FormatUint(epoch, 10), "bash", "-c", script, "dyna-stop-box")
	return err
}

func (h *Host) removeBoxNow(ctx context.Context, id contract.BoxID, volumes bool) error {
	epoch, err := h.BoxEpoch(ctx, id)
	if err != nil {
		return err
	}
	return h.RemoveBox(ctx, id, epoch, volumes, "")
}

// RemoveBox performs the entire destructive body in one host command under one
// stable lock. Absence of the workspace alone never skips the stack removal.
const removeSliceScript = `systemctl --user stop "$2" || [ "$(systemctl --user show "$2" -p LoadState --value)" = not-found ]
test -n "$XDG_RUNTIME_DIR" && rm -rf -- "$XDG_RUNTIME_DIR/systemd/user.control/$2.d" && systemctl --user daemon-reload`

func (h *Host) RemoveBox(ctx context.Context, id contract.BoxID, epoch uint64, volumes bool, slice string) error {
	if slice != "" {
		if _, err := sliceBoxID(slice); err != nil {
			return err
		}
	}
	script := "set -eu\n" + removeContainersCommand(id)
	if volumes {
		script += "\n" + removeVolumesCommand(id)
	}
	if slice != "" {
		script += "\n" + removeSliceScript
	}
	socket := (box.Layout{HostData: h.dataDir}).HostSocket(id)
	script += "\n" + shellCommand("rm", "-f", "--", socket) + "\n" + "[ ! -d " + quote(filepath.Dir(socket)) + " ] || " + shellCommand("rmdir", "--", filepath.Dir(socket))
	_, err := h.namespace(ctx, id, "delete", "--expect-epoch", strconv.FormatUint(epoch, 10), "bash", "-c", script, "dyna-remove-box", string(id), slice)
	if err == nil {
		h.forgetBoxForwards(id)
	}
	return err
}
