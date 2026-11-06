package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/contract"
)

// runnerEntrypoint is the shared image's ENTRYPOINT, passed on every start so a room's layer cannot replace it.
const runnerEntrypoint = `["/usr/bin/tini","--","/opt/dyna-ci/entrypoint.sh"]`

const (
	ciLabel         = "dyna.ci" // "runner" or "egress"
	runnerRepoLabel = "dyna.ci.repo"
	runnerIDLabel   = "dyna.ci.id"
	runnerSlotLabel = "dyna.ci.slot"
	runnerSizeLabel = "dyna.ci.size"
	// egressName names both the proxy container and the volume that carries its sockets.
	egressName  = "dyna-ci-egress"
	egressMount = "/run/dyna-egress"
	// runnerSlice sits under dyna.slice, so the heartbeat and the host's memory limit include runners.
	runnerSlice = "dyna-ci.slice"
	// runnerLimit is the host's own bound, in seconds. A job shares a user with the
	// entrypoint's timeout, so it can stop that clock. This one is outside the container.
	runnerLimit = "--timeout=22200" // 6 hours 10 minutes
	// cacheMount is where a runner reads the host's cache, the folder the host's boxes mount at /box/cache. It writes only its
	// slot's build folder there, which outlives the job, as upstream projects keep CARGO_TARGET_DIR on their own runners.
	cacheMount = "/cache"
	// buildMount is where a runner writes its slot's build folder, one path for every slot. sccache's keys hold the paths a
	// build hands the compiler, so the folder's cargo and target folders are mounted again where a box has them, and a job and
	// a box each hit what the other compiled.
	buildMount = "/build"
	// buildCacheMount holds the socket of the host's compiler cache, which the entrypoint relays to the port sccache uses.
	buildCacheMount = "/run/dyna-build-cache"
	// cacheLimitKB empties a build folder above 40 GiB before the next job starts.
	cacheLimitKB = 40 << 20
)

// buildFolder is a slot's build folder, relative to the host's cache.
func buildFolder(slot int) string { return path.Join("ci", strconv.Itoa(slot)) }

// buildScript makes a slot's build folder, owned by the runner's user, and empties it when it has grown past cacheLimitKB. du
// and rm run in Podman's user namespace, where the job's files live. Its cargo and target folders are mounts of their own.
const buildScript = `b="$1"; limit="$2"
mkdir -p -- "$b"
# The runner's user must pass through the cache root and ci/ to reach its folder; a box may have made them 700.
chmod a+rx -- "$(dirname -- "$(dirname -- "$b")")" "$(dirname -- "$b")"
kb=$(podman unshare du -sk "$b" | cut -f1)
if [ "$kb" -gt "$limit" ]; then podman unshare find "$b" -mindepth 1 -delete; fi
podman unshare mkdir -p -- "$b/cargo" "$b/target"
podman unshare chown 1001:1001 "$b" "$b/cargo" "$b/target"`

// BuildRunnerImage builds or reuses the shared runner image, and drops any older one. With a layer it builds the room's image on
// that and returns the room's tag, dropping the room's older ones. The shared image stays on the host as the fallback.
func (h *Host) BuildRunnerImage(ctx context.Context, layer *box.RunnerLayer) (string, error) {
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
	if _, err := h.build(ctx, tag, "", contextTar.Bytes(), false); err != nil {
		return "", err
	}
	// An image a running container still uses fails to delete, and goes with a later image.
	_, err := h.run(ctx, "bash", "-c", `podman images --format '{{.Repository}}:{{.Tag}}' | { grep '^localhost/dyna-ci-runner:' || true; } | { grep -vxF "$1" || true; } | xargs -r podman rmi >/dev/null 2>&1 || true`, "dyna", tag)
	if err != nil || layer == nil {
		return tag, err
	}
	if !runnerLayerName.MatchString(string(layer.Room)) || !runnerLayerID.MatchString(layer.ID) {
		return "", errors.New("invalid runner layer")
	}
	roomRepo := roomRunnerRepo(layer.Room)
	roomTag := fmt.Sprintf("%s:%s-%x", roomRepo, layer.ID, hash.Sum(nil)[:6])
	if _, err := h.buildWith(ctx, roomTag, "DYNA_CI_BASE", tag, layer.Context, false); err != nil {
		return "", err
	}
	_, err = h.run(ctx, "bash", "-c", `podman images --format '{{.Repository}}:{{.Tag}}' | { grep "^$1:" || true; } | { grep -vxF "$2" || true; } | xargs -r podman rmi >/dev/null 2>&1 || true`, "dyna", roomRepo, roomTag)
	return roomTag, err
}

// runnerLayerName and runnerLayerID are what a layer's room and id may hold, since both go into an image name.
var (
	runnerLayerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	runnerLayerID   = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

// runnerLayerRepoPrefix starts the name of every room layer image.
const runnerLayerRepoPrefix = "localhost/dyna-ci-room-"

// roomRunnerRepo is the image name of a room's runner layers.
func roomRunnerRepo(room contract.RoomID) string {
	return runnerLayerRepoPrefix + strings.ToLower(string(room))
}

func egressArgs(image, bin, sum string) []string {
	// A runner with no proxy holds its slot until its time limit, so a crashed proxy restarts by itself.
	return []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--replace", "--restart=always", "--name", egressName,
		"--label", ciLabel + "=egress", "--cgroup-parent=" + runnerSlice,
		// The proxy alone sees the host's network, and only to leave it for a public address.
		"--network=host", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1001:1001",
		"--cpus=4", "--memory=1g", "--memory-swap=1g", "--pids-limit=512",
		"--volume", egressName + ":" + egressMount,
		"--volume", filepath.Join(bin, sum) + ":/opt/dyna/bin/current:ro",
		"--entrypoint", "/opt/dyna/bin/current/dyna", image, "ci-egress", "--dir", egressMount}
}

// EnsureEgress starts the proxy that runners share on this host.
func (h *Host) EnsureEgress(ctx context.Context, image, bin, sum string) error {
	if err := binaryPath(bin, sum); err != nil {
		return err
	}
	ctx = WithRemoteCancellation(ctx)
	args := append([]string{"bash", "-c", pinCreateScript, "dyna-pin-create", bin}, egressArgs(image, bin, sum)...)
	if _, err := h.run(ctx, args...); err != nil {
		return err
	}
	// A runner's mount needs its slot's directory, which the proxy makes as it starts.
	_, err := h.run(ctx, "podman", "exec", egressName, "bash", "-c", `for _ in {1..100}; do [ -S "$1" ] && exit 0; sleep 0.1; done; exit 1`, "dyna-egress",
		fmt.Sprintf("%s/%d/%s", egressMount, box.RunnerSlots-1, box.EgressSocket))
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
	build := filepath.Join(s.Cache, buildFolder(s.Slot))
	work, _ := box.RepoFolder(s.Repo) // StartRunner checked the name
	target := path.Join(work, "target")
	return []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--rm", runnerLimit, "--name", s.Name,
		"--label", ciLabel + "=runner", "--label", runnerRepoLabel + "=" + string(s.Repo), "--label", runnerIDLabel + "=" + strconv.FormatInt(s.ID, 10),
		"--label", runnerSlotLabel + "=" + strconv.Itoa(s.Slot), "--label", runnerSizeLabel + "=" + string(s.Size.Name),
		"--cgroup-parent=" + runnerSlice,
		// A host's containers.conf can default to host networking, so this is always explicit.
		"--network=none", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1001:1001",
		"--cpus=" + strconv.Itoa(s.Size.CPUs), "--memory=" + memory, "--memory-swap=" + memory, "--pids-limit=" + strconv.Itoa(s.Size.Pids),
		"--tmpfs", "/home/runner:rw,exec,U,mode=0755," + disk,
		"--tmpfs", "/tmp:rw,exec,mode=1777," + disk,
		// Its slot alone: the proxy limits a job by socket, so a job must not reach another.
		"--mount", fmt.Sprintf("type=volume,source=%s,destination=%s,subpath=%d,ro", egressName, egressMount, s.Slot),
		// The host's cache is read-only, so a job cannot change the boxes' files, and its slot's folder alone is writable.
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s,ro", s.Cache, cacheMount),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", build, buildMount),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", filepath.Join(build, "cargo"), box.CargoHome),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", filepath.Join(build, "target"), target),
		// The compiler cache is the boxes' too, so a job writes what they read, which spec 5.6 accepts.
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s,ro", s.BuildCache, buildCacheMount),
		"--log-driver=k8s-file", "--log-opt=max-size=1m",
		"--env", "ACTIONS_RUNNER_INPUT_JITCONFIG", "--env", "DYNA_CI_REPO=" + string(s.Repo),
		"--env", "DYNA_CI_CACHE=" + cacheMount, "--env", "DYNA_CI_BUILD=" + buildMount,
		// A box of this repository sets the same two, and no other CARGO_ variable, which sccache hashes too.
		"--env", "CARGO_HOME=" + box.CargoHome, "--env", "CARGO_TARGET_DIR=" + target,
		// Dyna's entrypoint and user, whatever a room's layer sets. A layer can still change files in its own image.
		"--entrypoint", runnerEntrypoint,
		s.Image}
}

// StartRunner starts one ephemeral runner. It is gone when its job ends.
func (h *Host) StartRunner(ctx context.Context, s box.RunnerSpec) error {
	if _, err := box.RepoFolder(s.Repo); err != nil {
		return errors.New("invalid runner")
	}
	if s.Name == "" || strings.HasPrefix(s.Name, "-") || s.Image == "" || s.Config == "" || strings.ContainsAny(s.Config, "\n\x00") || s.Slot < 0 || s.Slot >= box.RunnerSlots {
		return errors.New("invalid runner")
	}
	if _, sized := box.Size(s.Size.Name); !sized || !filepath.IsAbs(s.Cache) || strings.ContainsAny(s.Cache, ",=\n\x00") || !filepath.IsAbs(s.BuildCache) || strings.ContainsAny(s.BuildCache, ",=\n\x00") {
		return errors.New("invalid runner")
	}
	if _, err := h.run(ctx, "bash", "-c", buildScript, "dyna-ci-build", filepath.Join(s.Cache, buildFolder(s.Slot)), strconv.Itoa(cacheLimitKB)); err != nil {
		return fmt.Errorf("prepare build folder: %w", err)
	}
	args := append([]string{"bash", "-c", `IFS= read -r ACTIONS_RUNNER_INPUT_JITCONFIG && export ACTIONS_RUNNER_INPUT_JITCONFIG && exec "$@"`, "dyna-runner"}, runnerArgs(s)...)
	// Podman's output says why it failed, and it never holds the config, which is only in its environment.
	out, err := h.runInput(ctx, []byte(s.Config+"\n"), args...)
	var exit remoteExit
	if errors.As(err, &exit) {
		// The bare status, since err ends with the output's last lines and only this masks the config in them.
		return fmt.Errorf("start runner: %w: %s", exit, strings.ReplaceAll(strings.TrimSpace(string(out)), s.Config, "***"))
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
		slot, err := strconv.Atoi(r.Labels[runnerSlotLabel])
		if err != nil {
			return nil, false, fmt.Errorf("runner %s: %w", r.Names[0], err)
		}
		// A runner an older engine started has no size, and was large unless its repository said otherwise.
		size, sized := box.Size(contract.BoxSizeName(r.Labels[runnerSizeLabel]))
		if !sized {
			size, _ = box.Size(contract.BoxSizeLarge)
		}
		runners = append(runners, box.Runner{Name: r.Names[0], Repo: contract.RepoName(r.Labels[runnerRepoLabel]), ID: id, Slot: slot, Size: size, Running: r.State == "running"})
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

// PruneRunnerLayers removes the room layer images of every room but these, as a room that dropped its ci-runner image, was
// deleted or left CI has no build to replace its image. An image a container still uses stays until a later prune.
func (h *Host) PruneRunnerLayers(ctx context.Context, keep []contract.RoomID) error {
	select {
	case h.buildGate <- struct{}{}:
		defer func() { <-h.buildGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	out, err := h.run(ctx, "podman", "images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return err
	}
	kept := make(map[string]bool, len(keep))
	for _, room := range keep {
		kept[roomRunnerRepo(room)] = true
	}
	for _, image := range strings.Fields(string(out)) {
		repo, _, _ := strings.Cut(image, ":")
		if strings.HasPrefix(repo, runnerLayerRepoPrefix) && !kept[repo] {
			_, _ = h.run(ctx, "podman", "rmi", image)
		}
	}
	return nil
}
