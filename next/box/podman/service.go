package podman

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// quadletDir and userUnitDir are the host folders of 4.3, under the Dyna user's home.
const (
	quadletDir  = ".config/containers/systemd"
	userUnitDir = ".config/systemd/user"
)

// writeFile writes the file from stdin and renames it into place, so a reader never sees half of it.
const writeFile = `d="$HOME/$1" && mkdir -p "$d" && cat > "$d/$2.tmp" && mv "$d/$2.tmp" "$d/$2" && systemctl --user daemon-reload`

// serviceHostScript prints one line per fact of the service host check.
const serviceHostScript = `echo "linger $(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)"
if [ -x /usr/lib/systemd/system-generators/podman-system-generator ] || [ -x /usr/libexec/podman/quadlet ] || [ -x /usr/lib/podman/quadlet ]; then echo "quadlet yes"; else echo "quadlet no"; fi
if command -v pasta >/dev/null 2>&1; then echo "pasta yes"; else echo "pasta no"; fi
echo "lowport $(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null)"`

func (h *Host) CheckServiceHost(ctx context.Context) (box.ServiceHostCheck, error) {
	out, err := h.run(ctx, "bash", "-c", serviceHostScript)
	if err != nil {
		return box.ServiceHostCheck{}, err
	}
	var check box.ServiceHostCheck
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "linger":
			if value != "yes" {
				check.Issues = append(check.Issues, contract.ServiceHostNoLinger)
			}
		case "quadlet":
			if value != "yes" {
				check.Issues = append(check.Issues, contract.ServiceHostNoQuadlet)
			}
		case "pasta":
			if value != "yes" {
				check.Issues = append(check.Issues, contract.ServiceHostNoPasta)
			}
		case "lowport":
			if check.LowestPort, err = strconv.Atoi(value); err != nil {
				return box.ServiceHostCheck{}, fmt.Errorf("service host check: lowest port %q", value)
			}
		}
	}
	return check, nil
}

func (h *Host) EnsureServiceSlice(ctx context.Context) error {
	_, err := h.runInput(ctx, []byte(ServiceSlice), "bash", "-c", writeFile, "dyna", userUnitDir, ServiceSliceName)
	return err
}

func (h *Host) ListServiceFiles(ctx context.Context) ([]box.ServiceFile, error) {
	out, err := h.run(ctx, "bash", "-c", `cd "$HOME/.config/containers/systemd" 2>/dev/null || exit 0; for f in dyna-svc-*.container; do [ -f "$f" ] && echo "$(sha256sum < "$f" | cut -d' ' -f1) $f"; done; exit 0`)
	if err != nil {
		return nil, err
	}
	var files []box.ServiceFile
	for _, line := range strings.Split(string(out), "\n") {
		sha, file, ok := strings.Cut(strings.TrimSpace(line), " ")
		if id, found := strings.CutPrefix(file, "dyna-svc-"); ok && found {
			files = append(files, box.ServiceFile{ID: contract.ServiceID(strings.TrimSuffix(id, ".container")), SHA: sha})
		}
	}
	return files, nil
}

func (h *Host) ListeningPorts(ctx context.Context) ([]contract.PortBinding, error) {
	out, err := h.run(ctx, "ss", "-H", "-ltun")
	if err != nil {
		return nil, err
	}
	var ports []contract.PortBinding
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		_, port, ok := cutLast(fields[4], ":")
		n, err := strconv.Atoi(port)
		if !ok || err != nil {
			continue
		}
		proto := contract.PortTCP
		if strings.HasPrefix(fields[0], "udp") {
			proto = contract.PortUDP
		}
		ports = append(ports, contract.PortBinding{Host: n, Proto: proto})
	}
	return ports, nil
}

func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func (h *Host) PullService(ctx context.Context, ref string) error {
	_, err := h.runUnbounded(ctx, nil, "podman", "pull", "--quiet", ref)
	return err
}

func (h *Host) ImageSize(ctx context.Context, ref string) (int64, error) {
	out, err := h.run(ctx, "podman", "image", "inspect", "--format", "{{.Size}}", ref)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// WriteServiceSecret hands the value to podman on stdin. The argv of the command holds the secret's name and no value, and no
// file is written for it.
func (h *Host) WriteServiceSecret(ctx context.Context, id contract.ServiceID, name string, value []byte) error {
	_, err := h.runInput(ctx, value, "podman", "secret", "create", "--replace", box.ServiceSecretName(id, name), "-")
	return err
}

func (h *Host) WriteServiceUnit(ctx context.Context, u box.ServiceUnitSpec) (string, error) {
	file := RenderServiceUnit(u)
	_, err := h.runInput(ctx, []byte(file), "bash", "-c", writeFile, "dyna", quadletDir, box.ServiceName(u.ID)+".container")
	return ServiceUnitSHA(file), err
}

func unit(id contract.ServiceID) string { return box.ServiceName(id) + ".service" }

func (h *Host) RestartService(ctx context.Context, id contract.ServiceID) error {
	_, err := h.runUnbounded(ctx, nil, "bash", "-c", `systemctl --user reset-failed "$1"; systemctl --user restart "$1"`, "dyna", unit(id))
	return err
}

func (h *Host) StopService(ctx context.Context, id contract.ServiceID) error {
	_, err := h.runUnbounded(ctx, nil, "systemctl", "--user", "stop", unit(id))
	return err
}

// serviceShow is the properties a service's state is read from, which the heartbeat reads too.
const serviceShow = "LoadState,ActiveState,SubState,NRestarts,Result"

func (h *Host) ShowService(ctx context.Context, id contract.ServiceID) (box.ServiceUnit, error) {
	out, err := h.run(ctx, "systemctl", "--user", "show", unit(id), "-p", serviceShow)
	if err != nil {
		return box.ServiceUnit{}, err
	}
	return parseServiceUnit(id, strings.Fields(strings.ReplaceAll(string(out), "\n", " ")))
}

// parseServiceUnit maps systemd's properties to a state, once, where they enter. A unit that is activating after its main
// process failed is restarting, and one that failed with start-limit-hit gave up.
func parseServiceUnit(id contract.ServiceID, fields []string) (box.ServiceUnit, error) {
	props := map[string]string{}
	for _, f := range fields {
		key, value, ok := strings.Cut(f, "=")
		if !ok {
			return box.ServiceUnit{}, fmt.Errorf("service %s: unit property %q", id, f)
		}
		props[key] = value
	}
	u := box.ServiceUnit{ID: id}
	u.Restarts, _ = strconv.Atoi(props["NRestarts"])
	switch {
	case props["LoadState"] == "not-found":
		u.State = box.UnitAbsent
	case props["ActiveState"] == "active" && props["SubState"] == "running":
		u.State = box.UnitRunning
	case props["ActiveState"] == "inactive":
		u.State = box.UnitStopped
	case props["ActiveState"] == "failed", props["ActiveState"] == "activating", props["ActiveState"] == "deactivating", props["ActiveState"] == "active":
		u.State = box.UnitCrashing
		u.CrashLoop = props["Result"] == "start-limit-hit"
	default:
		return box.ServiceUnit{}, fmt.Errorf("service %s: unit state %q", id, props["ActiveState"])
	}
	return u, nil
}

func (h *Host) ServiceHealthy(ctx context.Context, id contract.ServiceID, command []string) error {
	_, err := h.run(ctx, append([]string{"podman", "exec", box.ServiceName(id)}, command...)...)
	return err
}

func (h *Host) ServiceLogs(ctx context.Context, id contract.ServiceID, lines int, since time.Duration) (string, error) {
	args := []string{"journalctl", "--user", "-u", unit(id), "-o", "short-iso", "--no-pager", "-n", strconv.Itoa(lines)}
	if since > 0 {
		args = append(args, "--since", fmt.Sprintf("-%ds", int(since.Seconds())))
	}
	out, err := h.run(ctx, args...)
	return string(out), err
}

// removeService stops the unit and deletes what a service owns. Only absent objects are tolerated: other failures keep the
// removing row or leftover debt so the next pass retries. systemctl stop's status 5 means the unit is not loaded.
const removeService = `systemctl --user stop "$1.service" || { status=$?; [ "$status" = 5 ] || exit "$status"; }
rm -f "$HOME/.config/containers/systemd/$1.container" || exit
systemctl --user daemon-reload || exit
podman rm -f --ignore "$1" >/dev/null || exit
secrets=$(podman secret ls --format '{{.Name}}') || exit
while IFS= read -r s; do
  case "$s" in "$1"-*) podman secret rm "$s" >/dev/null || exit ;; esac
done <<<"$secrets"
podman volume rm -f "$1" >/dev/null || exit`

func (h *Host) RemoveService(ctx context.Context, id contract.ServiceID) error {
	_, err := h.runUnbounded(ctx, nil, "bash", "-c", removeService, "dyna", box.ServiceName(id))
	return err
}

var _ box.ServiceDriver = (*Host)(nil)
