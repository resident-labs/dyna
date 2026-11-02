package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const (
	// runLabel carries a Run's ID. It is not dyna.box, so the reconciler never sees a Run.
	runLabel = "dyna.run"
	// runSlice sits under dyna.slice, so the heartbeat and the host's memory limit include Runs.
	runSlice = "dyna-run.slice"
	// RunFolder is where a Run's files are, and the folder its command starts in.
	RunFolder = "/box/hook"
	// RunSocket is the engine socket inside a Run's box.
	RunSocket = "/run/dyna/engine/" + box.EngineSocket
	// runLogTail bounds what the engine reads back. The event keeps less.
	runLogTail = "400"
)

// runArgs is a small box: no volume, a read-only image, and memory-backed scratch space, so a Run can leave
// nothing behind but what it writes to the mounts it was given. It uses the session boxes' user namespace, so
// /box/shared has one owner and the image is already chowned for it. --timeout holds without the engine.
func runArgs(c box.RunContainer) []string {
	memory := strconv.FormatInt(c.Size.MemBytes, 10)
	// tmpfs pages count against the memory limit, so a full disk here is a write error, not an OOM kill.
	disk := fmt.Sprintf("size=%d", c.Size.MemBytes/2)
	args := []string{"podman", "--cgroup-manager=systemd", "run", "--detach", "--name", c.ID, boxUserNS,
		fmt.Sprintf("--timeout=%d", int(box.RunLimit.Seconds())),
		"--label", runLabel + "=" + c.ID, "--cgroup-parent=" + runSlice,
		// A host's containers.conf can default to host networking, so this is always explicit.
		"--network=pasta", "--read-only", "--cap-drop=all", "--security-opt=no-new-privileges", "--user", "1000:1000",
		"--cpus=" + strconv.Itoa(c.Size.CPUs), "--memory=" + memory, "--memory-swap=" + memory, "--pids-limit=" + strconv.Itoa(c.Size.Pids),
		"--tmpfs", "/box/home:rw,exec,U,mode=0755," + disk,
		"--tmpfs", "/tmp:rw,exec,mode=1777," + disk,
		"--log-driver=k8s-file", "--log-opt=max-size=1m",
		"--env", "HOME=/box/home", "--env", "DYNA_SOCKET=" + RunSocket,
		"--workdir", RunFolder, "--entrypoint", "/usr/bin/tini"}
	for _, m := range c.Mounts {
		v := m.HostPath + ":" + m.BoxPath
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}
	return append(append(args, c.Image, "--"), c.Command...)
}

// PutRunFiles writes a Run's files into its host folder, which the box mounts read-only.
func (h *Host) PutRunFiles(ctx context.Context, dir string, files []contract.RunFile) error {
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	for _, file := range files {
		if err := w.WriteHeader(&tar.Header{Name: file.Path, Mode: 0755, Size: int64(len(file.Body))}); err != nil {
			return err
		}
		if _, err := w.Write([]byte(file.Body)); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	_, err := h.runInput(ctx, archive.Bytes(), "bash", "-c", `mkdir -p -m 0700 -- "$1" && tar --no-same-owner --no-overwrite-dir -xf - -C "$1"`, "dyna", dir)
	return err
}

// StartRun starts one Run's box. The container stays after it exits, so its exit code and output can be read.
func (h *Host) StartRun(ctx context.Context, c box.RunContainer) error {
	if c.ID == "" || strings.HasPrefix(c.ID, "-") || c.Image == "" || len(c.Command) == 0 {
		return errors.New("invalid run")
	}
	for _, m := range c.Mounts {
		if err := h.makeMountFolder(ctx, m); err != nil {
			return err
		}
	}
	out, err := h.run(ctx, runArgs(c)...)
	var exit remoteExit
	if errors.As(err, &exit) {
		return fmt.Errorf("start run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return err
}

// HasImage says whether this host already holds a tag.
func (h *Host) HasImage(ctx context.Context, tag string) (bool, error) {
	_, err := h.run(ctx, "podman", "image", "exists", tag)
	var exit remoteExit
	if errors.As(err, &exit) && exit == 1 {
		return false, nil
	}
	return err == nil, err
}

// RunStatus reports a Run's container, with its exit code once it has one.
func (h *Host) RunStatus(ctx context.Context, id contract.BoxID) (box.RunStatus, error) {
	out, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label="+runLabel+"="+string(id), "--format", "json")
	if err != nil {
		return box.RunStatus{}, err
	}
	var records []struct {
		State    string `json:"State"`
		ExitCode int    `json:"ExitCode"`
	}
	if err := json.Unmarshal(out, &records); err != nil {
		return box.RunStatus{}, err
	}
	if len(records) == 0 {
		return box.RunStatus{}, nil
	}
	return box.RunStatus{Found: true, Running: records[0].State != "exited" && records[0].State != "stopped", ExitCode: records[0].ExitCode}, nil
}

// RunOutput reads the end of what a Run wrote to standard output and standard error, in order.
func (h *Host) RunOutput(ctx context.Context, id contract.BoxID) ([]byte, error) {
	return h.run(ctx, "bash", "-c", `podman logs --tail "$2" "$1" 2>&1`, "dyna", string(id), runLogTail)
}

// RemoveRun destroys a Run's container and folder, running or not.
func (h *Host) RemoveRun(ctx context.Context, id contract.BoxID, dir string) error {
	if id == "" || strings.HasPrefix(string(id), "-") {
		return errors.New("invalid run")
	}
	if _, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "rm", "--force", "--ignore", "--time", "0", string(id)); err != nil {
		return err
	}
	_, err := h.run(ctx, "rm", "-rf", "--", dir)
	return err
}

// ListRuns names the Run containers on this host, so a sweep can remove any that outlived their row.
func (h *Host) ListRuns(ctx context.Context) ([]contract.BoxID, error) {
	out, err := h.run(ctx, "podman", "--cgroup-manager=systemd", "ps", "--all", "--filter", "label="+runLabel, "--format", "json")
	if err != nil {
		return nil, err
	}
	var records []struct {
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(out, &records); err != nil {
		return nil, err
	}
	ids := make([]contract.BoxID, 0, len(records))
	for _, r := range records {
		ids = append(ids, contract.BoxID(r.Labels[runLabel]))
	}
	return ids, nil
}
