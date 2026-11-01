package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/contract"
)

const (
	ciLabel         = "dyna.ci" // "runner" or "egress"
	runnerRepoLabel = "dyna.ci.repo"
	runnerIDLabel   = "dyna.ci.id"
	// egressName names both the proxy container and the volume that carries its socket.
	egressName  = "dyna-ci-egress"
	egressMount = "/run/dyna-egress"
	// runnerSlice sits under dyna.slice, so the heartbeat and the host's memory limit include runners.
	runnerSlice = "dyna-ci.slice"
	// runnerLimit is the host's own bound, in seconds. A job shares a user with the
	// entrypoint's timeout, so it can stop that clock. This one is outside the container.
	runnerLimit = "--timeout=22200" // 6 hours 10 minutes
)

// BuildRunnerImage builds or reuses the runner image, and drops any older one.
func (h *Host) BuildRunnerImage(ctx context.Context) (string, error) {
	var contextTar bytes.Buffer
	tw := tar.NewWriter(&contextTar)
	hash := sha256.New()
	for _, name := range []string{"Containerfile", "entrypoint.sh", "job-started.sh"} {
		data, err := images.Runner.ReadFile("ci-runner/" + name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s %d\n", name, len(data))
		hash.Write(data)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data))}); err != nil {
			return "", err
		}
		if _, err := tw.Write(data); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	tag := fmt.Sprintf("localhost/dyna-ci-runner:%x", hash.Sum(nil))
	if err := h.build(ctx, tag, "", contextTar.Bytes(), false); err != nil {
		return "", err
	}
	// An image a running container still uses fails to delete, and goes with a later image.
	_, err := h.run(ctx, "bash", "-c", `podman images --format '{{.Repository}}:{{.Tag}}' | { grep '^localhost/dyna-ci-runner:' || true; } | { grep -vxF "$1" || true; } | xargs -r podman rmi >/dev/null 2>&1 || true`, "dyna", tag)
	return tag, err
}

func egressArgs(image, bin string) []string {
	// A runner with no proxy holds its slot until its time limit, so a crashed proxy restarts by itself.
	return []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--replace", "--restart=always", "--name", egressName,
		"--label", ciLabel + "=egress", "--cgroup-parent=" + runnerSlice,
		// The proxy alone sees the host's network, and only to leave it for a public address.
		"--network=host", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1001:1001",
		"--cpus=4", "--memory=1g", "--memory-swap=1g", "--pids-limit=512",
		"--volume", egressName + ":" + egressMount,
		"--volume", bin + ":/opt/dyna/bin:ro",
		"--entrypoint", "/opt/dyna/bin/current/dyna", image, "ci-egress", "--socket", egressMount + "/egress.sock"}
}

// EnsureEgress starts the proxy that runners share on this host.
func (h *Host) EnsureEgress(ctx context.Context, image, bin string) error {
	if !filepath.IsAbs(bin) {
		return errors.New("invalid binary folder")
	}
	_, err := h.run(ctx, egressArgs(image, bin)...)
	return err
}

// RemoveEgress removes the proxy and its volume after the last runner on this host.
func (h *Host) RemoveEgress(ctx context.Context) error {
	if _, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", "--force", "--ignore", "--time", "0", egressName); err != nil {
		return err
	}
	_, err := h.run(ctx, "podman", "volume", "rm", "--force", egressName)
	var exit remoteExit
	if errors.As(err, &exit) && exit == 1 {
		return nil
	}
	return err
}

// runnerArgs never carries the JIT config, which reaches podman on standard input.
func runnerArgs(s box.RunnerSpec) []string {
	memory := strconv.FormatInt(s.Size.MemBytes, 10)
	// tmpfs pages count against the memory limit, so one job can never hold more than that. Leaving
	// a quarter for processes means a full disk is an ordinary write error, not an OOM kill.
	disk := fmt.Sprintf("size=%d", s.Size.MemBytes/4*3)
	return []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--rm", runnerLimit, "--name", s.Name,
		"--label", ciLabel + "=runner", "--label", runnerRepoLabel + "=" + string(s.Repo), "--label", runnerIDLabel + "=" + strconv.FormatInt(s.ID, 10),
		"--cgroup-parent=" + runnerSlice,
		// A host's containers.conf can default to host networking, so this is always explicit.
		"--network=none", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1001:1001",
		"--cpus=" + strconv.Itoa(s.Size.CPUs), "--memory=" + memory, "--memory-swap=" + memory, "--pids-limit=" + strconv.Itoa(s.Size.Pids),
		"--tmpfs", "/home/runner:rw,exec,U,mode=0755," + disk,
		"--tmpfs", "/tmp:rw,exec,mode=1777," + disk,
		"--volume", egressName + ":" + egressMount + ":ro",
		"--log-driver=k8s-file", "--log-opt=max-size=1m",
		"--env", "ACTIONS_RUNNER_INPUT_JITCONFIG", "--env", "DYNA_CI_REPO=" + string(s.Repo),
		s.Image}
}

// StartRunner starts one ephemeral runner. It is gone when its job ends.
func (h *Host) StartRunner(ctx context.Context, s box.RunnerSpec) error {
	if s.Name == "" || strings.HasPrefix(s.Name, "-") || s.Repo == "" || s.Image == "" || s.Config == "" || strings.ContainsAny(s.Config, "\n\x00") {
		return errors.New("invalid runner")
	}
	args := append([]string{"bash", "-c", `IFS= read -r ACTIONS_RUNNER_INPUT_JITCONFIG && export ACTIONS_RUNNER_INPUT_JITCONFIG && exec "$@"`, "dyna-runner"}, runnerArgs(s)...)
	// Podman's output says why it failed, and it never holds the config, which is only in its environment.
	out, err := h.runInput(ctx, []byte(s.Config+"\n"), args...)
	var exit remoteExit
	if errors.As(err, &exit) {
		return fmt.Errorf("start runner: %w: %s", err, strings.ReplaceAll(strings.TrimSpace(string(out)), s.Config, "***"))
	}
	return err
}

// ListRunners reports this host's runner containers and whether its proxy is running.
func (h *Host) ListRunners(ctx context.Context) ([]box.Runner, bool, error) {
	out, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label="+ciLabel, "--format", "json")
	if err != nil {
		return nil, false, err
	}
	var records []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.Unmarshal(out, &records); err != nil {
		return nil, false, err
	}
	var runners []box.Runner
	egress := false
	for _, r := range records {
		if r.Labels[ciLabel] == "egress" {
			egress = egress || r.State == "running"
			continue
		}
		if len(r.Names) == 0 {
			return nil, false, errors.New("runner container has no name")
		}
		id, err := strconv.ParseInt(r.Labels[runnerIDLabel], 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("runner %s: %w", r.Names[0], err)
		}
		runners = append(runners, box.Runner{Name: r.Names[0], Repo: contract.RepoName(r.Labels[runnerRepoLabel]), ID: id, Running: r.State == "running"})
	}
	return runners, egress, nil
}

// RemoveRunner destroys a runner container, running or not.
func (h *Host) RemoveRunner(ctx context.Context, name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return errors.New("invalid runner name")
	}
	_, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", "--force", "--ignore", "--time", "0", name)
	return err
}
