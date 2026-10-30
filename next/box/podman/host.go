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
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// Runner is the local SSH process boundary.
type Runner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "ssh", args...)
	c.Stdin = strings.NewReader(string(in))
	return c.Output()
}

type Host struct {
	mu                  sync.Mutex
	destination, socket string
	runner              Runner
	forwards            map[box.Forward]struct{}
	restored            bool
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
	return &Host{destination: destination, socket: socket, runner: runner, forwards: make(map[box.Forward]struct{})}, nil
}
func (h *Host) base() []string {
	return []string{"-S", h.socket, "-o", "ControlMaster=no", "-o", "StreamLocalBindUnlink=yes"}
}
func (h *Host) control(ctx context.Context, op string, extra ...string) error {
	args := append(h.base(), "-O", op)
	args = append(args, extra...)
	args = append(args, h.destination)
	_, err := h.runner.Run(ctx, nil, args...)
	return err
}
func (h *Host) ensure(ctx context.Context) error {
	if h.control(ctx, "check") != nil {
		h.restored = false
		args := []string{"-M", "-N", "-f", "-S", h.socket, "-o", "ControlMaster=yes", "-o", "ControlPersist=10m", "-o", "StreamLocalBindUnlink=yes", h.destination}
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
		if err := h.control(ctx, "forward", "-R", f.HostPath+":"+f.EnginePath); err != nil {
			for _, done := range fs[:i] {
				_ = h.control(ctx, "cancel", "-R", done.HostPath+":"+done.EnginePath)
			}
			return err
		}
	}
	h.restored = true
	return nil
}
func (h *Host) AddForward(ctx context.Context, f box.Forward) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.ContainsAny(f.HostPath+f.EnginePath, ":\n") || !filepath.IsAbs(f.HostPath) || !filepath.IsAbs(f.EnginePath) {
		return errors.New("invalid forward path")
	}
	if err := h.ensure(ctx); err != nil {
		return err
	}
	if _, ok := h.forwards[f]; ok {
		return nil
	}
	if err := h.control(ctx, "forward", "-R", f.HostPath+":"+f.EnginePath); err != nil {
		return err
	}
	h.forwards[f] = struct{}{}
	return nil
}
func (h *Host) RemoveForward(ctx context.Context, f box.Forward) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.forwards[f]; !ok {
		return nil
	}
	if err := h.ensure(ctx); err != nil {
		return err
	}
	if err := h.control(ctx, "cancel", "-R", f.HostPath+":"+f.EnginePath); err != nil {
		return err
	}
	delete(h.forwards, f)
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
"$@" < "$tmp/in" > "$tmp/out"
status=$?
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensure(ctx); err != nil {
		return nil, err
	}
	// Do not retry a mutation after an uncertain reply; reconcile containers instead.
	parts := []string{"bash", "-c", frameScript, "dyna-frame"}
	parts = append(parts, argv...)
	for i := range parts {
		parts[i] = quote(parts[i])
	}
	args := append(h.base(), h.destination, strings.Join(parts, " "))
	out, sshErr := h.runner.Run(ctx, []byte("DYNA-IN 00000000000000000000\n"), args...)
	body, status, err := parseReply(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", err, sshErr)
	}
	if status != 0 {
		return nil, remoteExit(status)
	}
	return body, nil
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
	if _, err := h.run(ctx, "systemctl", "--user", "stop", name); err != nil {
		return err
	}
	_, err := h.run(ctx, "bash", "-c", `rm -rf -- "$XDG_RUNTIME_DIR/systemd/user.control/$1.d"; systemctl --user daemon-reload`, "dyna", name)
	return err
}
func (h *Host) CreateContainer(ctx context.Context, c box.ContainerSpec) error {
	args := []string{"podman", "--cgroup-manager=systemd", "create", "--name", string(c.Box), "--label", "dyna.box=" + string(c.Box), "--cgroup-parent=" + c.Slice, "--cgroupns=private", "--systemd=always", "--stop-signal", "SIGTERM", "--entrypoint", "/usr/bin/tini", "--tmpfs", "/run/dyna", "--env", "HOME=/box/home", "--env", "PI_CODING_AGENT_DIR=/box/pi"}
	for _, m := range c.Mounts {
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
	_, err := h.run(ctx, args...)
	return err
}
func (h *Host) StartContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "start", string(id))
	return err
}
func (h *Host) StopContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "stop", string(id))
	return err
}
func (h *Host) RemoveContainer(ctx context.Context, id contract.BoxID) error {
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", string(id))
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
		out = append(out, box.ContainerStatus{Box: contract.BoxID(r.Labels["dyna.box"]), Running: r.State == "running"})
	}
	return out, nil
}

var _ box.HostDriver = (*Host)(nil)
