// Package podman runs boxes on a host through an SSH control master.
package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Runner is the local SSH process boundary.
type Runner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdin = strings.NewReader(string(in))
	out, err := c.Output()
	// ssh says why it failed only on stderr; without it an error reads "exit status 255".
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

type Host struct {
	buildGate           chan struct{}
	shortGate           chan struct{}
	hostexecGate        chan struct{}
	stopGate            chan struct{}
	gate                chan struct{}
	destination, socket string
	runner              Runner
	forwards            map[box.Forward]struct{}
	restored            bool
	masterReset         bool
	generation          atomic.Uint64
}

// New returns one driver per host. runtime is Dyna v2's private runtime directory.
func New(destination, runtime, hostKey string, runner Runner) (*Host, error) {
	if destination == "" || strings.HasPrefix(destination, "-") || hostKey == "" || filepath.Base(hostKey) != hostKey {
		return nil, errors.New("invalid SSH host")
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	dir := filepath.Join(runtime, "dyna-next", "ssh")
	socket := filepath.Join(dir, hostKey)
	if len(socket) >= 100 {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("dyna-ssh-%d", os.Getuid()))
		socket = filepath.Join(dir, hostKey)
	}
	if len(socket) >= 100 {
		return nil, errors.New("SSH control socket path too long")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("insecure SSH control socket directory")
	}
	return &Host{gate: make(chan struct{}, 1), buildGate: make(chan struct{}, 1), shortGate: make(chan struct{}, 5), hostexecGate: make(chan struct{}, 3), stopGate: make(chan struct{}, 1), destination: destination, socket: socket, runner: runner, forwards: make(map[box.Forward]struct{})}, nil
}

// Generation changes each time the SSH master and its forwards are restored.
func (h *Host) Generation() uint64 { return h.generation.Load() }

func (h *Host) lock(ctx context.Context) error {
	select {
	case h.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *Host) unlock() { <-h.gate }
func (h *Host) base() []string {
	return []string{"ssh", "-S", h.socket, "-o", "ControlMaster=no", "-o", "StreamLocalBindUnlink=yes"}
}

// TerminalArgv uses the same SSH transport as other host operations.
func (h *Host) TerminalArgv(id contract.BoxID) []string {
	args := append(h.base(), "-tt", h.destination)
	if id != "" {
		args = append(args, "podman", "exec", "-it", "-e", "TERM=xterm-256color", string(id), "bash", "-l")
	}
	return args
}

func (h *Host) control(ctx context.Context, op string, extra ...string) error {
	args := append(h.base(), "-O", op)
	args = append(args, extra...)
	args = append(args, h.destination)
	_, err := h.runner.Run(ctx, nil, args...)
	return err
}
func (h *Host) ensure(ctx context.Context) error {
	if !h.masterReset {
		_ = h.control(ctx, "exit")
		if err := ctx.Err(); err != nil {
			return err
		}
		h.masterReset = true
	}
	if h.control(ctx, "check") != nil {
		h.restored = false
		if err := ctx.Err(); err != nil {
			return err
		}
		// A master killed without cleanup leaves its socket. A new master keeps that socket and runs
		// without multiplexing, so every later check fails and starts one more master.
		if err := os.Remove(h.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		args := []string{"ssh", "-M", "-N", "-f", "-S", h.socket, "-o", "ControlMaster=yes", "-o", "ControlPersist=10m", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-o", "StreamLocalBindUnlink=yes", h.destination}
		if _, err := h.runner.Run(ctx, nil, args...); err != nil {
			return err
		}
	}
	if h.restored {
		return nil
	}
	fs := make([]box.Forward, 0, len(h.forwards))
	for f := range h.forwards {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].HostPath < fs[j].HostPath })
	for i, f := range fs {
		// The dead master's socket is still on the host (see AddForward).
		_, err := h.execute(ctx, "rm", "-f", "--", f.HostPath)
		if err == nil {
			err = h.control(ctx, "forward", "-R", f.HostPath+":"+f.EnginePath)
		}
		if err != nil {
			for _, done := range fs[:i] {
				_ = h.control(ctx, "cancel", "-R", done.HostPath+":"+done.EnginePath)
			}
			return err
		}
	}
	h.restored = true
	h.generation.Add(1)
	return nil
}
func validateForward(f box.Forward) error {
	if strings.ContainsAny(f.HostPath+f.EnginePath, ":\n") || !filepath.IsAbs(f.HostPath) || !filepath.IsAbs(f.EnginePath) {
		return errors.New("invalid forward path")
	}
	if f.Box != "" && (filepath.Base(f.HostPath) != box.EngineSocket || filepath.Base(filepath.Dir(f.HostPath)) != string(f.Box)) {
		return errors.New("invalid box socket path")
	}
	return nil
}
func (h *Host) AddForward(ctx context.Context, f box.Forward) error {
	if err := validateForward(f); err != nil {
		return err
	}
	if err := h.lock(ctx); err != nil {
		return err
	}
	defer h.unlock()
	if err := h.ensure(ctx); err != nil {
		return err
	}
	if _, ok := h.forwards[f]; ok {
		return nil
	}
	if f.Box != "" {
		// A socket left by an earlier master, as after an engine restart, makes the forward fail:
		// sshd keeps it, because its StreamLocalBindUnlink defaults to no.
		if _, err := h.execute(ctx, "bash", "-c", `mkdir -p -- "$1" && chmod 0700 -- "$1" && rm -f -- "$2"`, "dyna", filepath.Dir(f.HostPath), f.HostPath); err != nil {
			return err
		}
	}
	if err := h.control(ctx, "forward", "-R", f.HostPath+":"+f.EnginePath); err != nil {
		return err
	}
	h.forwards[f] = struct{}{}
	return nil
}
func (h *Host) RemoveForward(ctx context.Context, f box.Forward) error {
	if err := validateForward(f); err != nil {
		return err
	}
	if err := h.lock(ctx); err != nil {
		return err
	}
	defer h.unlock()
	if err := h.ensure(ctx); err != nil {
		return err
	}
	if _, ok := h.forwards[f]; ok {
		if err := h.control(ctx, "cancel", "-R", f.HostPath+":"+f.EnginePath); err != nil {
			return err
		}
		delete(h.forwards, f)
	}
	if f.Box != "" {
		_, err := h.execute(ctx, "bash", "-c", `rm -f -- "$1" && { [ ! -d "$2" ] || rmdir -- "$2"; }`, "dyna", f.HostPath, filepath.Dir(f.HostPath))
		return err
	}
	return nil
}

const frameScript = `set -u
tmp=$(mktemp -d) || exit 254
trap 'rm -rf "$tmp"' EXIT
IFS= read -r header || exit 254
case "$header" in "DYNA-IN "*) size=${header#DYNA-IN } ;; *) exit 254 ;; esac
case "$size" in *[!0-9]*|'') exit 254 ;; esac
[ "${#size}" = 20 ] || exit 254
size=$((10#$size))
head -c "$size" > "$tmp/in" || exit 254
[ "$(wc -c < "$tmp/in")" -eq "$size" ] || exit 254
"$@" < "$tmp/in" > "$tmp/out" 2> "$tmp/err"
status=$?
if [ "$status" -eq 0 ]; then cat "$tmp/err" >&2; else cat "$tmp/err" >> "$tmp/out"; fi
cat "$tmp/out"
n=$(wc -c < "$tmp/out")
printf 'DYNA-END %020d %03d\n' "$n" "$status"
`

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func parseReply(b []byte) ([]byte, int, error) {
	const n = 34
	if len(b) < n {
		return nil, 0, errors.New("SSH transport: missing reply trailer")
	}
	t := string(b[len(b)-n:])
	if !strings.HasPrefix(t, "DYNA-END ") || t[29] != ' ' || t[33] != '\n' {
		return nil, 0, errors.New("SSH transport: invalid reply trailer")
	}
	for _, c := range t[9:29] + t[30:33] {
		if c < '0' || c > '9' {
			return nil, 0, errors.New("SSH transport: invalid reply trailer")
		}
	}
	size, err := strconv.ParseInt(t[9:29], 10, 64)
	if err != nil || size != int64(len(b)-n) {
		return nil, 0, errors.New("SSH transport: invalid reply length")
	}
	status, _ := strconv.Atoi(t[30:33])
	if status > 255 {
		return nil, 0, errors.New("SSH transport: invalid reply status")
	}
	return b[:len(b)-n], status, nil
}

type remoteExit int

func (e remoteExit) Error() string { return fmt.Sprintf("host command exited with status %d", e) }

func (h *Host) run(ctx context.Context, argv ...string) ([]byte, error) {
	return h.runInput(ctx, nil, argv...)
}
func (h *Host) runInput(ctx context.Context, input []byte, argv ...string) ([]byte, error) {
	select {
	case h.shortGate <- struct{}{}:
		defer func() { <-h.shortGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return h.runUnbounded(ctx, input, argv...)
}
func (h *Host) runUnbounded(ctx context.Context, input []byte, argv ...string) ([]byte, error) {
	if err := h.lock(ctx); err != nil {
		return nil, err
	}
	err := h.ensure(ctx)
	h.unlock()
	if err != nil {
		return nil, err
	}
	return h.executeInput(ctx, input, argv...)
}
func (h *Host) execute(ctx context.Context, argv ...string) ([]byte, error) {
	return h.executeInput(ctx, nil, argv...)
}
func (h *Host) executeInput(ctx context.Context, input []byte, argv ...string) ([]byte, error) {
	// Do not retry a mutation after an uncertain reply; reconcile containers instead.
	parts := []string{"bash", "-c", frameScript, "dyna-frame"}
	parts = append(parts, argv...)
	for i := range parts {
		parts[i] = quote(parts[i])
	}
	args := append(h.base(), h.destination, strings.Join(parts, " "))
	frame := append([]byte(fmt.Sprintf("DYNA-IN %020d\n", len(input))), input...)
	out, sshErr := h.runner.Run(ctx, frame, args...)
	body, status, err := parseReply(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", err, sshErr)
	}
	if status != 0 {
		return body, remoteExit(status)
	}
	return body, nil
}

// Maintain removes dangling images and compacts caches without deleting borrowed objects.
func (h *Host) Maintain(ctx context.Context, layout box.Layout) ([]contract.RoomShared, error) {
	out, err := h.runUnbounded(ctx, nil, "bash", "-c", `podman image prune -f >/dev/null || exit; for repo in "$1"/*/*.git; do [ -d "$repo" ] || continue; git -C "$repo" gc --no-prune || exit; done; for dir in "$2"/*/"$3"; do [ -d "$dir" ] || continue; size=$(du -sb -- "$dir") || exit; room=${dir%/*}; printf '%s\0%s\0' "${room##*/}" "${size%%$'\t'*}"; done`, "dyna-maintain", layout.HostGitRoot(), layout.HostRooms(), box.SharedFolder)
	if err != nil {
		return nil, err
	}
	var sizes []contract.RoomShared
	parts := strings.Split(string(out), "\x00")
	if len(parts)%2 != 1 || parts[len(parts)-1] != "" {
		return nil, errors.New("invalid room size output")
	}
	for i := 0; i < len(parts)-1; i += 2 {
		bytes, err := strconv.ParseInt(parts[i+1], 10, 64)
		if err != nil || bytes < 0 || parts[i] == "" {
			return nil, errors.New("invalid room size output")
		}
		sizes = append(sizes, contract.RoomShared{Room: contract.RoomID(parts[i]), Bytes: bytes})
	}
	return sizes, nil
}

// RemoveRoom deletes a room's folder. It runs in the user namespace, since a box can leave read-only folders and files
// under a subordinate ID that the host user cannot remove. Its size has no bound, so it takes no short-command slot.
func (h *Host) RemoveRoom(ctx context.Context, dir string) error {
	_, err := h.runUnbounded(ctx, nil, "podman", "unshare", "rm", "-rf", "--", dir)
	return err
}

func (h *Host) EnsureSlice(ctx context.Context, s box.Slice) error {
	if _, err := h.run(ctx, "systemctl", "--user", "start", s.Name); err != nil {
		var exit remoteExit
		if !errors.As(err, &exit) {
			return err
		}
	}
	_, err := h.run(ctx, "systemctl", "--user", "set-property", "--runtime", s.Name,
		fmt.Sprintf("CPUQuota=%d%%", s.CPUs*100), fmt.Sprintf("MemoryHigh=%d", s.MemHigh), fmt.Sprintf("MemoryMax=%d", s.MemMax), fmt.Sprintf("TasksMax=%d", s.Pids))
	return err
}
func (h *Host) RemoveSlice(ctx context.Context, name string) error {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".slice") {
		return errors.New("invalid slice name")
	}
	if _, err := h.run(ctx, "bash", "-c", `systemctl --user stop "$1" || [ "$(systemctl --user show "$1" -p LoadState --value)" = not-found ]`, "dyna", name); err != nil {
		return err
	}
	_, err := h.run(ctx, "bash", "-c", `test -n "$XDG_RUNTIME_DIR" && rm -rf -- "$XDG_RUNTIME_DIR/systemd/user.control/$1.d" && systemctl --user daemon-reload`, "dyna", name)
	return err
}
func (h *Host) CreateContainer(ctx context.Context, c box.ContainerSpec) error {
	args := []string{"podman", "--cgroup-manager=systemd", "create", "--name", string(c.Box), boxUserNS, "--user", "1000:1000", "--label", "dyna.box=" + string(c.Box), "--cgroup-parent=" + c.Slice, "--cgroupns=private", "--systemd=always", "--stop-signal", "SIGTERM", "--entrypoint", "/usr/bin/tini", "--tmpfs", "/run/dyna:mode=1777", "--env", "HOME=/box/home", "--env", "PI_CODING_AGENT_DIR=/box/pi", "--volume", "dyna-box-" + string(c.Box) + ":/box"}
	for _, m := range c.Mounts {
		if !m.ReadOnly || m.MakeDir {
			if _, err := h.run(ctx, "bash", "-c", `if [ ! -e "$1" ]; then mkdir -p -m 0700 -- "$1"; fi`, "dyna", m.HostPath); err != nil {
				return err
			}
		}
		v := m.HostPath + ":" + m.BoxPath
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}
	for _, e := range c.Env {
		args = append(args, "--env", e.Name+"="+e.Value)
	}
	for _, l := range c.Labels {
		args = append(args, "--label", l.Key+"="+l.Value)
	}
	args = append(args, c.Image, "--", "/opt/dyna/bin/current/dyna", "box-agent")
	var checks string
	for _, need := range c.Needs {
		switch need {
		case box.NeedNestedContainers:
			checks += "test -c /dev/net/tun || { printf nested-containers; exit 254; }; "
			args = append(args[:len(args)-4], append([]string{"--device", "/dev/net/tun", "--security-opt", "unmask=/proc/sys"}, args[len(args)-4:]...)...)
		case box.NeedKVM:
			checks += "test -c /dev/kvm || { printf kvm; exit 254; }; "
			args = append(args[:len(args)-4], append([]string{"--device", "/dev/kvm", "--group-add", "keep-groups"}, args[len(args)-4:]...)...)
		}
	}
	if checks == "" {
		_, err := h.run(ctx, args...)
		return err
	}
	command := append([]string{"bash", "-c", checks + `exec "$@"`, "dyna-devices"}, args...)
	out, err := h.run(ctx, command...)
	var exit remoteExit
	if errors.As(err, &exit) && exit == 254 {
		switch string(out) {
		case string(box.NeedNestedContainers):
			return box.MissingDevice{Need: box.NeedNestedContainers}
		case string(box.NeedKVM):
			return box.MissingDevice{Need: box.NeedKVM}
		}
	}
	return err
}
func (h *Host) StartContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "start", string(id))
	return err
}
func (h *Host) StopContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "stop", "--ignore", string(id))
	return err
}
func (h *Host) RemoveContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", "--ignore", "--volumes", string(id))
	return err
}
func (h *Host) RemoveVolume(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "volume", "rm", "dyna-box-"+string(id))
	// Podman documents status 1 as a missing volume, and 2 or 125 as errors.
	var exit remoteExit
	if errors.As(err, &exit) && exit == 1 {
		return nil
	}
	return err
}
func (h *Host) ListContainers(ctx context.Context) ([]box.ContainerStatus, error) {
	b, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label=dyna.box", "--format", "json")
	if err != nil {
		return nil, err
	}
	var records []struct {
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, err
	}
	out := make([]box.ContainerStatus, 0, len(records))
	for _, r := range records {
		out = append(out, box.ContainerStatus{Box: contract.BoxID(r.Labels["dyna.box"]), Running: r.State == "running", StaleLayout: r.Labels["dyna.layout"] != box.ContainerLayout})
	}
	return out, nil
}

var _ box.HostDriver = (*Host)(nil)

// MachineID reads the host identity over the authenticated SSH transport.
func (h *Host) MachineID(ctx context.Context) (string, error) {
	b, err := h.run(ctx, "cat", "/etc/machine-id")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(b))
	if len(value) != 32 || strings.Trim(value, "0123456789abcdef") != "" {
		return "", errors.New("invalid host machine-id")
	}
	return value, nil
}
