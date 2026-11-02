package podman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
)

type CheckName string

const (
	CheckAdmin     CheckName = "owner-only administration"
	CheckPaths     CheckName = "paths"
	CheckCgroup    CheckName = "cgroup v2 and delegated controllers"
	CheckArch      CheckName = "host architecture"
	CheckPodman    CheckName = "rootless Podman 5 with systemd"
	CheckGit       CheckName = "git"
	CheckCapacity  CheckName = "capacity"
	CheckHostSlice CheckName = "host slice"
	CheckForward   CheckName = "stream-local forwarding"
	CheckShare     CheckName = "FUSE for the shared folder"
)

// RequiredError reports a missing or unsupported rootless Podman installation.
type RequiredError struct{}

func (RequiredError) Error() string {
	return "Install rootless Podman 5 or later with systemd cgroups on the host"
}

type CheckItem struct {
	Name   CheckName
	OK     bool
	Reason string
}
type CheckResult struct {
	Items               []CheckItem
	CPUs                int
	MemBytes, DiskBytes int64
	NoSwap              bool
}

func (r CheckResult) Ready() bool {
	if len(r.Items) == 0 {
		return false
	}
	for _, i := range r.Items {
		if !i.OK {
			return false
		}
	}
	return true
}

// Check performs the registration probes. AdminOnly is the owner's assertion,
// not a claim that SSH can verify.
type CheckOptions struct {
	Layout                 box.Layout
	Runtime                string
	AdminOnly              bool
	EngineHost             bool // The engine host's boxes bind the shared folders directly, so it needs no FUSE.
	MemoryFloor, DiskFloor int64
}

func (h *Host) Check(ctx context.Context, o CheckOptions) CheckResult {
	var result CheckResult
	if o.MemoryFloor == 0 {
		o.MemoryFloor = 4 << 30
	}
	if o.DiskFloor == 0 {
		o.DiskFloor = 20 << 30
	}
	add := func(name CheckName, err error) {
		item := CheckItem{Name: name, OK: err == nil}
		if err != nil {
			item.Reason = err.Error()
		}
		result.Items = append(result.Items, item)
	}
	if !o.AdminOnly {
		add(CheckAdmin, fmt.Errorf("owner did not confirm exclusive administration"))
	} else {
		add(CheckAdmin, nil)
	}
	if !filepath.IsAbs(o.Layout.HostData) || !filepath.IsAbs(o.Runtime) {
		add(CheckPaths, fmt.Errorf("host data and runtime paths must be absolute"))
		return result
	}
	add(CheckArch, h.checkArch(ctx))
	_, err := h.run(ctx, "bash", "-c", `test "$(stat -fc %T /sys/fs/cgroup)" = cgroup2fs && test "$(systemctl show "user@$(id -u).service" -p Delegate --value)" = yes && cg=$(systemctl show "user@$(id -u).service" -p ControlGroup --value) && for c in cpu memory pids; do grep -qw "$c" "/sys/fs/cgroup$cg/cgroup.controllers" || exit 1; done`)
	add(CheckCgroup, err)
	_, err = h.run(ctx, "bash", "-c", `test "$(podman info --format '{{.Host.Security.Rootless}}')" = true && test "$(podman info --format '{{.Host.CgroupManager}}')" = systemd && test "$(podman version --format '{{.Client.Version}}' | cut -d. -f1)" -ge 5`)
	add(CheckPodman, err)
	_, err = h.run(ctx, "git", "--version")
	add(CheckGit, err)
	result.CPUs, result.MemBytes, result.DiskBytes, err = h.capacity(ctx, o)
	add(CheckCapacity, err)
	if err == nil {
		var output []byte
		output, err = h.run(ctx, "awk", `/^SwapTotal:/ {print $2; found=1} END {if (!found) exit 1}`, "/proc/meminfo")
		if err == nil {
			var swap int64
			swap, err = strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
			result.NoSwap = swap == 0
		}
		add(CheckName("swap"), err)
	}
	if err == nil {
		_, err = h.run(ctx, "systemctl", "--user", "set-property", "--runtime", "dyna.slice", fmt.Sprintf("MemoryMax=%d", result.MemBytes), fmt.Sprintf("MemoryHigh=%d", result.MemBytes/10*9+(result.MemBytes%10)*9/10))
		add(CheckHostSlice, err)
	}
	add(CheckForward, h.checkForward(ctx, o))
	if !o.EngineHost {
		add(CheckShare, h.checkShare(ctx))
	}
	return result
}

// capacity creates the host data directory before reading its free space.
func (h *Host) capacity(ctx context.Context, o CheckOptions) (int, int64, int64, error) {
	output, err := h.run(ctx, "bash", "-c", `set -e -o pipefail; mkdir -p -m 0700 -- "$1"; getconf _NPROCESSORS_ONLN; awk '/MemTotal:|MemAvailable:/ {printf "%.0f\n", $2*1024}' /proc/meminfo; df -PB1 -- "$1" | awk 'NR==2 {print $4}'`, "probe", o.Layout.HostData)
	if err != nil {
		return 0, 0, 0, err
	}
	return parseCapacity(output, o.MemoryFloor, o.DiskFloor)
}

func (h *Host) checkArch(ctx context.Context) error {
	output, err := h.run(ctx, "uname", "-m")
	if err != nil {
		return err
	}
	arch := strings.TrimSpace(string(output))
	if (arch == "x86_64" && runtime.GOARCH == "amd64") || (arch == "aarch64" && runtime.GOARCH == "arm64") {
		return nil
	}
	return fmt.Errorf("host architecture %q does not match engine architecture %q", arch, runtime.GOARCH)
}

// parseCapacity validates the host probe and reserves the host floor once.
func parseCapacity(output []byte, memoryFloor, diskFloor int64) (int, int64, int64, error) {
	fields := strings.Fields(string(output))
	if len(fields) != 4 {
		return 0, 0, 0, fmt.Errorf("invalid capacity probe")
	}
	cpus, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, 0, err
	}
	total, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	available, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	disk, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	if cpus < 1 || total <= memoryFloor || available < memoryFloor || disk < diskFloor {
		return 0, 0, 0, fmt.Errorf("host capacity below floor")
	}
	return max(1, cpus-1), total - memoryFloor, disk, nil
}

func (h *Host) checkForward(ctx context.Context, o CheckOptions) error {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(raw[:])
	dir, err := os.MkdirTemp(filepath.Dir(h.socket), "p-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", local)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/probe" {
			fmt.Fprint(w, token)
			return
		}
		http.NotFound(w, r)
	})}
	go server.Serve(listener)
	defer server.Close()
	remote := o.Layout.HostProbeSocket(token)
	if _, err = h.run(ctx, "mkdir", "-p", filepath.Dir(remote)); err != nil {
		return err
	}
	f := box.Forward{HostPath: remote, EnginePath: local}
	if err = h.AddForward(ctx, f); err != nil {
		return err
	}
	output, err := h.run(ctx, "curl", "--fail", "--silent", "--unix-socket", remote, "http://probe/probe")
	cancelErr := h.RemoveForward(ctx, f)
	if err != nil {
		return fmt.Errorf("curl 7.40 or later and stream-local forwarding are required: %w", err)
	}
	if cancelErr != nil {
		return fmt.Errorf("cancel probe forward: %w", cancelErr)
	}
	if string(output) != token {
		return fmt.Errorf("forward returned the wrong token")
	}
	return nil
}
