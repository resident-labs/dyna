package podman

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const (
	// stackLabel marks sidecars and compose volumes with their box. It is not dyna.box, so box status never reads
	// a sidecar as a box.
	stackLabel = "dyna.stack"
	// StackHashLabel records on a box container which stack it was made for.
	StackHashLabel = "dyna.stack.hash"
	orderLabel     = "dyna.stack.order"
	waitLabel      = "dyna.stack.wait"
	// infraCommand keeps the pod's network namespace. Without it and --infra-image, Podman needs catatonit on the host,
	// which host registration does not ask for. Podman splits this on spaces.
	infraCommand = "/usr/bin/tini -- sleep infinity"
)

func stackVolumeName(id contract.BoxID, name string) string {
	return "dyna-box-" + string(id) + "-" + name
}

func stackVolumeFlag(id contract.BoxID, v box.Volume) string {
	flag := stackVolumeName(id, v.Name) + ":" + v.Target
	if v.ReadOnly {
		flag += ":ro"
	}
	return flag
}

func podArgs(id contract.BoxID, slice string, s *box.Stack) []string {
	// --share-parent=false keeps every container directly in the box's slice, under its limits. Podman's own pod cgroup
	// fails to start under a slice with a dash in its name, which systemd reads as a parent. The infra process stays
	// outside the slice, and holds nothing but the namespace.
	// Only the network is shared, so a sidecar has its own IPC and UTS. The network is always named, because a host's
	// containers.conf can default to the host's own.
	args := []string{"podman", "--cgroup-manager=systemd", "pod", "create", "--name", box.PodName(id), boxUserNS,
		"--cgroup-parent=" + slice, "--share-parent=false", "--share=net", "--network=pasta",
		"--label", stackLabel + "=" + string(id),
		"--infra-name", box.PodName(id) + "-infra", "--infra-image=" + s.Infra, "--infra-command=" + infraCommand}
	for _, name := range s.Names {
		args = append(args, "--add-host", name+":127.0.0.1")
	}
	return args
}

func sidecarArgs(id contract.BoxID, slice string, order int, c box.Sidecar) []string {
	// PullImage has fetched the image with no sign-in, so this must not fetch it again with one.
	args := []string{"podman", "--cgroup-manager=systemd", "create", "--name", box.SidecarName(id, c.Name), "--pod", box.PodName(id), "--cgroup-parent=" + slice, "--pull=never",
		"--label", stackLabel + "=" + string(id), "--label", orderLabel + "=" + strconv.Itoa(order), "--label", waitLabel + "=" + strconv.Itoa(int(c.Wait/time.Second))}
	for _, e := range c.Env {
		args = append(args, "--env", e.Name+"="+e.Value)
	}
	for _, v := range c.Volumes {
		args = append(args, "--volume", stackVolumeFlag(id, v))
	}
	if c.CPUs > 0 {
		args = append(args, "--cpus="+strconv.FormatFloat(c.CPUs, 'f', -1, 64))
	}
	if c.MemBytes > 0 {
		args = append(args, "--memory="+strconv.FormatInt(c.MemBytes, 10))
	}
	if c.Pids > 0 {
		args = append(args, "--pids-limit="+strconv.Itoa(c.Pids))
	}
	if len(c.Health) > 0 {
		// Dyna runs the check itself at start. Podman's schedule would add a timer and a Podman command per sidecar,
		// every interval, for as long as the box runs.
		args = append(args, "--health-cmd="+healthCommand(c.Health), "--health-interval=disable")
		if c.HealthTimeout > 0 {
			args = append(args, "--health-timeout="+c.HealthTimeout.String())
		}
	}
	args = append(args, c.Image)
	return append(args, c.Command...)
}

// healthCommand spells a compose test for Podman. Podman splits an array that starts with CMD on spaces, which breaks it,
// and takes any other array of two or more words as the command to run, so CMD is left out. A one-word test runs in a shell.
func healthCommand(test []string) string {
	if test[0] == "CMD" {
		test = test[1:]
	}
	encoded, _ := json.Marshal(test) // a slice of strings cannot fail
	return string(encoded)
}

// stackStep runs one Podman command and keeps its output when it fails, because that says why a stack cannot run, such as
// a host where pasta cannot start. Podman's own message holds nothing of a sidecar's.
func (h *Host) stackStep(ctx context.Context, what string, argv ...string) error {
	out, err := h.run(ctx, argv...)
	var exit remoteExit
	if errors.As(err, &exit) {
		return box.StackError{Reason: fmt.Sprintf("%s: %s", what, strings.TrimSpace(string(out[max(0, len(out)-512):])))}
	}
	return err
}

// CreateStack creates a box's compose volumes, its pod and its sidecars, all stopped. A volume that exists stays as it is,
// so a swap keeps its data. A stack with no sidecars needs no pod.
func (h *Host) CreateStack(ctx context.Context, id contract.BoxID, slice string, s *box.Stack) error {
	for _, name := range s.VolumeNames() {
		if err := h.stackStep(ctx, "create volume "+name, "podman", "volume", "create", "--ignore", "--label", stackLabel+"="+string(id), stackVolumeName(id, name)); err != nil {
			return err
		}
	}
	if len(s.Sidecars) == 0 {
		return nil
	}
	if err := h.stackStep(ctx, "create pod", podArgs(id, slice, s)...); err != nil {
		return err
	}
	for i, sidecar := range s.Sidecars {
		if err := h.stackStep(ctx, "create sidecar "+sidecar.Name, sidecarArgs(id, slice, i, sidecar)...); err != nil {
			return err
		}
	}
	return nil
}

type stackContainer struct {
	Names    []string          `json:"Names"`
	Labels   map[string]string `json:"Labels"`
	State    string            `json:"State"`
	ExitCode int               `json:"ExitCode"`
}

func (h *Host) sidecars(ctx context.Context, id contract.BoxID) ([]stackContainer, error) {
	out, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label="+stackLabel+"="+string(id), "--format", "json")
	if err != nil {
		return nil, err
	}
	var listed []stackContainer
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, err
	}
	// The infra container carries the pod's labels and has no order.
	sidecars := slices.DeleteFunc(listed, func(c stackContainer) bool { return len(c.Names) == 0 || c.Labels[orderLabel] == "" })
	order := func(c stackContainer) int { n, _ := strconv.Atoi(c.Labels[orderLabel]); return n }
	slices.SortFunc(sidecars, func(a, b stackContainer) int { return cmp.Compare(order(a), order(b)) })
	return sidecars, nil
}

// StartSidecars starts a box's sidecars in order, each once the one before it is up, so whatever a sidecar depends on is
// ready before it and all of them are ready before the box. A sidecar already running stays as it is.
func (h *Host) StartSidecars(ctx context.Context, id contract.BoxID) error {
	sidecars, err := h.sidecars(ctx, id)
	if err != nil {
		return err
	}
	if len(sidecars) == 0 {
		return nil
	}
	for _, sidecar := range sidecars {
		name := sidecar.Names[0]
		if err := h.stackStep(ctx, "start sidecar "+name, "podman", "--cgroup-manager=systemd", "start", name); err != nil {
			return err
		}
		if wait, _ := strconv.Atoi(sidecar.Labels[waitLabel]); wait > 0 {
			if err := h.awaitHealthy(ctx, name, time.Duration(wait)*time.Second); err != nil {
				return err
			}
		}
	}
	// A sidecar with no healthcheck that dies at once has still passed `podman start`.
	sidecars, err = h.sidecars(ctx, id)
	if err != nil {
		return err
	}
	for _, sidecar := range sidecars {
		if sidecar.State != "running" {
			return box.StackError{Reason: fmt.Sprintf("sidecar %s is not running: %s, exit code %d", sidecar.Names[0], sidecar.State, sidecar.ExitCode)}
		}
	}
	return nil
}

func (h *Host) awaitHealthy(ctx context.Context, name string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "healthcheck", "run", name)
		if err == nil {
			return nil
		}
		var exit remoteExit
		if !errors.As(err, &exit) {
			return err
		}
		if time.Now().After(deadline) {
			return box.StackError{Reason: fmt.Sprintf("sidecar %s was not healthy within %s", name, wait)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// StopPod stops a box's pod, sidecars and all. A box without one needs nothing.
func (h *Host) StopPod(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "pod", "stop", "--ignore", box.PodName(id))
	return err
}

// RemovePod removes a box's pod and every container in it, the box too, and keeps the volumes.
func (h *Host) RemovePod(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "pod", "rm", "--force", "--ignore", "--time", "0", box.PodName(id))
	return err
}

// RemoveStackVolumes removes a box's compose volumes. It comes after RemovePod, because prune leaves a volume in use.
func (h *Host) RemoveStackVolumes(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "volume", "prune", "--force", "--filter", "label="+stackLabel+"="+string(id))
	return err
}

// PullImage fetches a sidecar's image and warms it, as Build does for a box image. It uses no sign-in the host may hold: a
// compose file is untrusted, so it reaches only what the public can.
func (h *Host) PullImage(ctx context.Context, ref string) error {
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err := h.run(ctx, "podman", "image", "exists", ref)
	if err == nil {
		return nil
	}
	var exit remoteExit
	if !errors.As(err, &exit) || exit != 1 {
		return err
	}
	// A pull has no bound, so like a build it takes no short-command slot.
	out, err := h.runUnbounded(ctx, nil, "bash", "-c", `exec podman pull --quiet --authfile=<(printf '{}') "$1"`, "dyna-pull", ref)
	if errors.As(err, &exit) {
		return box.StackError{Reason: fmt.Sprintf("pull %s: %s", ref, strings.TrimSpace(string(out[max(0, len(out)-512):])))}
	}
	if err != nil {
		return err
	}
	return h.warm(ctx, ref)
}
