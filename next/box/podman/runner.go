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
	ciLabel          = "dyna.ci" // "runner" or "egress"
	binaryLabel      = "dyna.binary"
	runnerRepoLabel  = "dyna.ci.repo"
	runnerIDLabel    = "dyna.ci.id"
	runnerSlotLabel  = "dyna.ci.slot"
	runnerSizeLabel  = "dyna.ci.size"
	runnerBuildLabel = "dyna.ci.private-build"
	// egressName names both the proxy container and the volume that carries its sockets.
	egressName  = "dyna-ci-egress"
	egressMount = "/run/dyna-egress"
	// runnerSlice sits under dyna.slice, so the heartbeat and the host's memory limit include runners.
	runnerSlice = "dyna-ci.slice"
	// runnerLimit is the host's own bound, in seconds. A job shares a user with the
	// entrypoint's timeout, so it can stop that clock. This one is outside the container.
	runnerLimit = "--timeout=22200" // 6 hours 10 minutes
	// cacheMount is where a runner reads the host's cache, the folder the host's boxes mount at /box/cache.
	cacheMount = "/cache"
	// buildMount is where a runner writes its slot's build folder, one path for every slot. sccache's keys hold the paths a
	// build hands the compiler, so the folder's cargo and target folders are mounted again where a box has them, and a job and
	// a box each hit what the other compiled.
	buildMount = "/build"
	// keepMount is a host-local cache shared only by runners of this repository.
	keepMount   = "/keep"
	keepLimitKB = 20 << 20 // 20 GiB per repository, checked before each runner starts.
	// buildCacheMount holds the socket of the host's compiler cache, which the entrypoint relays to the port sccache uses.
	buildCacheMount = "/run/dyna-build-cache"
	// cacheLimitKB is the size a build folder may have when the next job starts, at most: 80 GiB. buildScript lowers it to what the
	// host's disk can hold in every slot at once, and never under 40 GiB.
	cacheLimitKB = 80 << 20
)

// buildScript makes a slot's build folder, owned by the runner's user. The last repository is recorded beside the
// folder, outside the mount, so a job cannot claim to be the next repository. Only jobs of the same repo reuse files.
// A folder grown past cacheLimitKB loses its compiled output first, since the compiler cache can rebuild it, and only if
// still over the limit is it emptied. du and rm run in Podman's user namespace, where the job's files live.
const buildScript = `set -eu -o pipefail
b="$1"; repo="$2"; limit="$3"; slots="$4"; cache="$5"; keep_root="$6"
# A box or the compiler-cache service may have created the cache without traverse permission.
mkdir -p -- "$cache"
chmod u+x -- "$cache"
pending() { echo 'legacy CI slot folders remain in the shared cache; waiting for migration' >&2; exit 1; }
if [ -L "$cache/ci" ]; then pending; fi
if [ -e "$cache/ci" ]; then
  [ -d "$cache/ci" ] && [ -x "$cache/ci" ] || pending
  for ((slot=0; slot<slots; slot++)); do
    if [ -e "$cache/ci/$slot" ] || [ -L "$cache/ci/$slot" ]; then pending; fi
  done
fi
chmod a+rx -- "$cache"
mkdir -p -m 0700 -- "$(dirname -- "$b")"
if [ ! -f "$b.repo" ] || [ "$(< "$b.repo")" != "$repo" ]; then
  podman unshare rm -rf -- "$b"
fi
podman unshare mkdir -p -- "$b"
# Every slot at the limit must fit in the disk above twice its floor (box.DiskFloor), as in pruneRunnerFoldersScript.
disk=$(df -Pk -- "$b" | tail -1)
set -- $disk
floor=$(( $2 / 10 )); [ "$floor" -ge 20971520 ] || floor=20971520
# Other runners may rename files during du. Use the total it could see; pruning checks actual free space.
keep_used=0
if [ -d "$keep_root" ]; then
  keep_used=$(podman unshare du -sk -- "$keep_root" 2>/dev/null || true)
  keep_used=${keep_used%%$'\t'*}
fi
share=$(( ($2 - 2 * floor - ${keep_used:-0}) / slots )); [ "$share" -ge 41943040 ] || share=41943040
[ "$limit" -le "$share" ] || limit=$share
used=$(podman unshare du -sk -- "$b" | cut -f1)
if [ "$used" -gt "$limit" ]; then
  podman unshare rm -rf -- "$b/target"
  used=$(podman unshare du -sk -- "$b" | cut -f1)
  if [ "$used" -gt "$limit" ]; then podman unshare rm -rf -- "$b"; fi
fi
# Only real directories may become the extra bind mounts, never links left by a job.
podman unshare mkdir -p -- "$b"
podman unshare find "$b" -mindepth 1 -maxdepth 1 \( -name cargo -o -name target \) ! -type d -delete
podman unshare mkdir -p -- "$b/cargo" "$b/target"
podman unshare chown 1001:1001 "$b" "$b/cargo" "$b/target"
printf '%s\n' "$repo" > "$b.repo"`

// keepScript must run under runnerMu. A repo with another container holding its cache can keep writing it;
// check again on the next start instead of emptying that mount underneath it.
const keepScript = `set -eu -o pipefail
k="$1"; repo="$2"; limit="$3"
mkdir -p -m 0700 -- "$(dirname -- "$k")"
if [ -d "$k" ]; then
  held=$(podman ps --all --filter "label=dyna.ci.repo=$repo" --format '{{.ID}}')
  if [ -n "$held" ]; then exit 0; fi
  used=$(podman unshare du -sk -- "$k" | cut -f1)
  if [ "$used" -gt "$limit" ]; then podman unshare rm -rf -- "$k"; fi
fi
podman unshare mkdir -p -- "$k"
podman unshare chown 1001:1001 "$k"`

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
		"--label", ciLabel + "=egress", "--label", binaryLabel + "=" + sum, "--cgroup-parent=" + runnerSlice,
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
	// A runner's mount needs its slot's directory and a live listener, not a stale socket inode from --replace.
	_, err := h.run(ctx, "podman", "exec", egressName, "bash", "-c", `for _ in {1..100}; do socat -T 1 -u /dev/null "UNIX-CONNECT:$1" 2>/dev/null && exit 0; sleep 0.1; done; exit 1`, "dyna-egress",
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
func runnerArgs(s box.RunnerSpec, build, keep, work string) []string {
	memory := strconv.FormatInt(s.Size.MemBytes, 10)
	// tmpfs pages count against the memory limit, so one job can never hold more than that. Leaving
	// a quarter for processes means a full disk is an ordinary write error, not an OOM kill.
	disk := fmt.Sprintf("size=%d", s.Size.MemBytes/4*3)
	target := path.Join(work, "target")
	return []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--rm", runnerLimit, "--name", s.Name,
		"--label", ciLabel + "=runner", "--label", runnerRepoLabel + "=" + string(s.Repo), "--label", runnerIDLabel + "=" + strconv.FormatInt(s.ID, 10),
		"--label", runnerSlotLabel + "=" + strconv.Itoa(s.Slot), "--label", runnerSizeLabel + "=" + string(s.Size.Name),
		"--label", runnerBuildLabel + "=1",
		"--cgroup-parent=" + runnerSlice,
		// A host's containers.conf can default to host networking, so this is always explicit.
		"--network=none", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1001:1001",
		"--cpus=" + strconv.Itoa(s.Size.CPUs), "--memory=" + memory, "--memory-swap=" + memory, "--pids-limit=" + strconv.Itoa(s.Size.Pids),
		"--tmpfs", "/home/runner:rw,exec,U,mode=0755," + disk,
		"--tmpfs", "/tmp:rw,exec,mode=1777," + disk,
		// Its slot alone: the proxy limits a job by socket, so a job must not reach another.
		"--mount", fmt.Sprintf("type=volume,source=%s,destination=%s,subpath=%d,ro", egressName, egressMount, s.Slot),
		// The host's cache is read-only. Only this slot's build and this repository's keep are mounted writable.
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s,ro", s.Cache, cacheMount),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", build, buildMount),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", filepath.Join(build, "cargo"), box.CargoHome),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", filepath.Join(build, "target"), target),
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s", keep, keepMount),
		// The compiler cache is the boxes' too, so a job writes what they read, which spec 5.6 accepts.
		"--mount", fmt.Sprintf("type=bind,source=%s,destination=%s,ro", s.BuildCache, buildCacheMount),
		"--log-driver=k8s-file", "--log-opt=max-size=1m",
		"--env", "ACTIONS_RUNNER_INPUT_JITCONFIG", "--env", "DYNA_CI_REPO=" + string(s.Repo),
		"--env", "DYNA_CI_CACHE=" + cacheMount, "--env", "DYNA_CI_BUILD=" + buildMount, "--env", "DYNA_CI_KEEP=" + keepMount,
		// A box of this repository sets the same two, and no other CARGO_ variable, which sccache hashes too.
		"--env", "CARGO_HOME=" + box.CargoHome, "--env", "CARGO_TARGET_DIR=" + target,
		// Dyna's entrypoint and user, whatever a room's layer sets. A layer can still change files in its own image.
		"--entrypoint", runnerEntrypoint,
		s.Image}
}

// StartRunner starts one ephemeral runner. It is gone when its job ends.
func (h *Host) StartRunner(ctx context.Context, s box.RunnerSpec) error {
	h.runnerMu.Lock()
	defer h.runnerMu.Unlock()
	work, err := box.RepoFolder(s.Repo)
	if err != nil {
		return errors.New("invalid runner")
	}
	if s.Name == "" || strings.HasPrefix(s.Name, "-") || s.Image == "" || s.Config == "" || strings.ContainsAny(s.Config, "\n\x00") || s.Slot < 0 || s.Slot >= box.RunnerSlots {
		return errors.New("invalid runner")
	}
	layout := box.Layout{HostData: h.dataDir}
	if _, sized := box.Size(s.Size.Name); !sized || !filepath.IsAbs(s.Cache) || !filepath.IsAbs(s.BuildCache) ||
		strings.ContainsAny(s.Cache+s.BuildCache+h.dataDir, ",=\n\x00") {
		return errors.New("invalid runner")
	}
	build := filepath.Join(layout.HostCIBuild(), strconv.Itoa(s.Slot))
	// RepoFolder checked both halves of the name. Neither can contain '+', so this key is reversible.
	keepRoot := layout.HostCIKeep()
	keep := filepath.Join(keepRoot, strings.Replace(string(s.Repo), "/", "+", 1))
	ctx = WithRemoteCancellation(ctx)
	if _, err := h.run(ctx, "bash", "-c", keepScript, "dyna-ci-keep", keep, string(s.Repo), strconv.Itoa(keepLimitKB)); err != nil {
		return fmt.Errorf("prepare keep folder: %w", err)
	}
	if _, err := h.run(ctx, "bash", "-c", buildScript, "dyna-ci-build", build, string(s.Repo), strconv.Itoa(cacheLimitKB), strconv.Itoa(box.RunnerSlots), s.Cache, keepRoot); err != nil {
		return fmt.Errorf("prepare build folder: %w", err)
	}
	args := append([]string{"bash", "-c", `IFS= read -r ACTIONS_RUNNER_INPUT_JITCONFIG && export ACTIONS_RUNNER_INPUT_JITCONFIG && exec "$@"`, "dyna-runner"}, runnerArgs(s, build, keep, work)...)
	// Podman's output says why it failed, and it never holds the config, which is only in its environment.
	out, err := h.runInput(ctx, []byte(s.Config+"\n"), args...)
	var exit remoteExit
	if errors.As(err, &exit) {
		// The bare status, since err ends with the output's last lines and only this masks the config in them.
		return fmt.Errorf("start runner: %w: %s", exit, strings.ReplaceAll(strings.TrimSpace(string(out)), s.Config, "***"))
	}
	return err
}

// pruneRunnerFoldersScript keeps the slot folders, which carry one job's build to the next job on that slot, while the
// host's disk has room. Under twice the disk floor (box.DiskFloor) it removes canonical slot directories without a
// container, highest slot first because new runners take the lowest free slot, until there is room again. With "all"
// it removes every such directory whatever the disk holds, for a box or service that waits for disk.
// Old slot folders in the shared cache go even when there is room. Move a held folder out of view without copying
// or breaking a running container's binds, then remove it once the old container is gone. Include stopped containers
// and fail closed if Podman cannot list them. This never touches named volumes.
const pruneRunnerFoldersScript = `set -eu -o pipefail
root="$1"; slots="$2"; all="$3"; legacy="$4"; keep="$5"; check_repos="$6"; retired="$root/.legacy"
shift 6
configured=$(printf '%s\n' "$@")
slot_holders() {
  podman ps --all --filter label=dyna.ci.slot --format '{{.Label "dyna.ci.slot"}} {{.Label "dyna.ci.private-build"}}'
}
roomy() {
  [ "$all" != all ] || return 1
  set -- $(df -Pk -- "$1" | tail -1)
  floor=$(( $2 / 10 )); [ "$floor" -ge 20971520 ] || floor=20971520
  [ "$4" -ge $(( 2 * floor )) ]
}
if [ -d "$legacy" ] || [ -d "$retired" ]; then
  holders=$(slot_holders)
  old_held=$(awk '$2 != "1" { print $1 }' <<< "$holders")
  (
    # Stay in the old directory once opened: the shared cache must not redirect cleanup through a link.
    cd -P -- "$(dirname -- "$legacy")" 2>/dev/null || exit 0
    old="$PWD/$(basename -- "$legacy")"
    [ ! -L "$old" ] && cd -P -- "$old" 2>/dev/null && [ "$PWD" = "$old" ] || exit 0
    for ((slot=slots-1; slot>=0; slot--)); do
      [ -e "./$slot" ] || [ -L "./$slot" ] || continue
      if ! grep -qxF -- "$slot" <<< "$old_held"; then
        podman unshare rm -rf -- "./$slot"
      elif [ ! -e "$retired/$slot" ] && [ ! -L "$retired/$slot" ]; then
        mkdir -p -m 0700 -- "$root" "$retired"
        # Rename without copying or deleting any live files. A directory that moves to another parent changes its own ".."
        # entry, so the rename runs as the folder's owner, in Podman's user namespace. Podman enters it by pathname, so perl
        # enters the old directory again and checks it before renaming. If it fails, new starts wait for the old runner.
        podman unshare perl -MCwd -e 'chdir($ARGV[0]) && getcwd() eq $ARGV[0] && rename($ARGV[1], $ARGV[2]) or exit 1' -- "$old" "./$slot" "$retired/$slot" 2>/dev/null || true
      fi
    done
  )
  rmdir -- "$legacy" 2>/dev/null || true
  if [ -d "$retired" ]; then
    for ((slot=slots-1; slot>=0; slot--)); do
      if ! grep -qxF -- "$slot" <<< "$old_held" && { [ -e "$retired/$slot" ] || [ -L "$retired/$slot" ]; }; then
        podman unshare rm -rf -- "$retired/$slot"
      fi
    done
    rmdir -- "$retired" 2>/dev/null || true
  fi
fi
if [ -d "$root" ] && ! roomy "$root"; then
  if [ -z "${holders+x}" ]; then holders=$(slot_holders); fi
  held=$(awk '{ print $1 }' <<< "$holders")
  for ((slot=slots-1; slot>=0; slot--)); do
    if ! grep -qxF -- "$slot" <<< "$held" && { [ -e "$root/$slot" ] || [ -L "$root/$slot" ]; }; then
      podman unshare rm -rf -- "$root/$slot" "$root/$slot.repo"
      if roomy "$root"; then break; fi
    fi
  done
fi
# Remove idle keep folders only after giving slot folders a chance to reclaim disk. A repo whose CI
# is off does not need its cache even when the disk has room.
[ -d "$keep" ] || exit 0
pressure=true; if roomy "$keep"; then pressure=false; fi
for k in "$keep"/*; do
  [ -d "$k" ] && [ ! -L "$k" ] || continue
  key=${k##*/}
  [[ "$key" == *+* ]] || continue
  repo=${key/+//}
  if ! $pressure && { [ "$check_repos" != check ] || grep -qxF -- "$repo" <<< "$configured"; }; then continue; fi
  if [ -z "${held_repos+x}" ]; then
    held_repos=$(podman ps --all --filter label=dyna.ci.repo --format '{{.Label "dyna.ci.repo"}}')
  fi
  if ! grep -qxF -- "$repo" <<< "$held_repos"; then
    podman unshare rm -rf -- "$k"
    if $pressure && roomy "$keep"; then pressure=false; fi
  fi
done`

// pruneRunnerFolders requires runnerMu, so preparing a slot cannot race its cleanup.
func (h *Host) pruneRunnerFolders(ctx context.Context, all bool, repos []contract.RepoName) error {
	layout := box.Layout{HostData: h.dataDir}
	check := ""
	if repos != nil {
		check = "check"
	}
	args := []string{"bash", "-c", pruneRunnerFoldersScript, "dyna-ci-prune",
		layout.HostCIBuild(), strconv.Itoa(box.RunnerSlots), map[bool]string{true: "all"}[all], filepath.Join(layout.HostCache(), "ci"), layout.HostCIKeep(), check}
	for _, repo := range repos {
		args = append(args, string(repo))
	}
	_, err := h.runUnbounded(WithRemoteCancellation(ctx), nil, args...)
	return err
}

// ListRunners reclaims idle slot and keep folders on a host short of disk, and all of them when something waits for disk.
// It also removes idle keep folders for repositories no longer served by the pool. It reports both runner containers and
// the proxy container's state and pinned binary checksum.
func (h *Host) ListRunners(ctx context.Context, reclaim bool, repos []contract.RepoName) ([]box.Runner, box.Egress, error) {
	h.runnerMu.Lock()
	defer h.runnerMu.Unlock()
	// Take inventory first. A runner that exits during pruning must still be counted this pass so the pool
	// comes back for any folders it held, even if all repositories have turned CI off.
	out, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label="+ciLabel, "--format", "json")
	if err != nil {
		return nil, box.Egress{}, err
	}
	var records []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.Unmarshal(out, &records); err != nil {
		return nil, box.Egress{}, err
	}
	var runners []box.Runner
	var egress box.Egress
	for _, r := range records {
		if r.Labels[ciLabel] == "egress" {
			if egress.Present {
				egress.Running = egress.Running && r.State == "running"
				if r.Labels[binaryLabel] != egress.Binary {
					egress.Binary = ""
				}
			} else {
				egress = box.Egress{Present: true, Running: r.State == "running", Binary: r.Labels[binaryLabel]}
			}
			continue
		}
		if len(r.Names) == 0 {
			return nil, box.Egress{}, errors.New("runner container has no name")
		}
		id, err := strconv.ParseInt(r.Labels[runnerIDLabel], 10, 64)
		if err != nil {
			return nil, box.Egress{}, fmt.Errorf("runner %s: %w", r.Names[0], err)
		}
		slot, err := strconv.Atoi(r.Labels[runnerSlotLabel])
		if err != nil {
			return nil, box.Egress{}, fmt.Errorf("runner %s: %w", r.Names[0], err)
		}
		// A runner an older engine started has no size, and was large unless its repository said otherwise.
		size, sized := box.Size(contract.BoxSizeName(r.Labels[runnerSizeLabel]))
		if !sized {
			size, _ = box.Size(contract.BoxSizeLarge)
		}
		runners = append(runners, box.Runner{Name: r.Names[0], Repo: contract.RepoName(r.Labels[runnerRepoLabel]), ID: id, Slot: slot, Size: size, Running: r.State == "running"})
	}
	if err := h.pruneRunnerFolders(ctx, reclaim, repos); err != nil {
		return nil, box.Egress{}, err
	}
	return runners, egress, nil
}

// RemoveRunner destroys a runner container, running or not.
func (h *Host) RemoveRunner(ctx context.Context, name string) error {
	h.runnerMu.Lock()
	defer h.runnerMu.Unlock()
	if name == "" || strings.HasPrefix(name, "-") {
		return errors.New("invalid runner name")
	}
	if _, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", "--force", "--ignore", "--time", "0", name); err != nil {
		return err
	}
	return h.pruneRunnerFolders(ctx, false, nil)
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
