package podman

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// NamespaceSuperseded means a newer creation owns these names. The old deleter
// is complete, not retryable; it must not acquire the new epoch and try again.
type NamespaceSuperseded struct{ Box contract.BoxID }

func (e NamespaceSuperseded) Error() string {
	return "box " + string(e.Box) + ": deletion superseded by a newer host epoch"
}

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

// FD 8 is intentionally inherited by every mutating child. In particular, do
// not use flock --close or close this descriptor at a nested exec boundary.
// Both files live outside all container, workspace, stack and room trees.
const namespaceScript = `set -eu
root=$1; id=$2; mode=$3; shift 3
mkdir -p -- "$root/locks"
exec 8> "$root/locks/box-$id.lock"
flock -x -w 15 8 || exit 252
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
"$@"`

func (h *Host) namespace(ctx context.Context, id contract.BoxID, mode string, command ...string) ([]byte, error) {
	args := []string{"bash", "-c", namespaceScript, "dyna-box-namespace", h.dataDir, string(id), mode}
	out, err := h.run(ctx, append(args, command...)...)
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
	script += shellCommand(args...)
	out, err := h.namespace(ctx, c.Box, "create", "bash", "-c", script, "dyna-create-box", string(c.Box))
	var exit remoteExit
	if errors.As(err, &exit) && exit == 253 {
		return box.StackError{Reason: err.Error()}
	}
	return containerFailure(out, err)
}

func removeContainersCommand(id contract.BoxID) string {
	return strings.Join([]string{
		shellCommand("podman", "--cgroup-manager=systemd", "stop", "--ignore", string(id)),
		shellCommand("podman", "--cgroup-manager=systemd", "rm", "--ignore", "--volumes", string(id)),
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
	_, err := h.namespace(ctx, id, "delete", "--expect-epoch", strconv.FormatUint(epoch, 10), "podman", "--cgroup-manager=systemd", "stop", "--ignore", string(id))
	return err
}

func (h *Host) removeBoxNow(ctx context.Context, id contract.BoxID, volumes bool) error {
	epoch, err := h.BoxEpoch(ctx, id)
	if err != nil {
		return err
	}
	return h.RemoveBox(ctx, id, epoch, volumes)
}

// RemoveBox performs the entire destructive body in one host command under one
// stable lock. Absence of the workspace alone never skips the stack removal.
func (h *Host) RemoveBox(ctx context.Context, id contract.BoxID, epoch uint64, volumes bool) error {
	script := "set -eu\n" + removeContainersCommand(id)
	if volumes {
		script += "\n" + removeVolumesCommand(id)
	}
	_, err := h.namespace(ctx, id, "delete", "--expect-epoch", strconv.FormatUint(epoch, 10), "bash", "-c", script, "dyna-remove-box", string(id))
	return err
}
