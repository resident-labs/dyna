package agentslice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/mode"
)

// Rooms get their own slice below dyna.slice, so one room's memory and process
// count cannot take the machine or the other rooms down. The kernel enforces
// the limits: MemoryHigh throttles and reclaims, MemoryMax bounds the OOM kill
// to the room, and TasksMax makes a fork inside the room fail while the
// desktop and every other room keep working.

// RoomSlicePrefix names the slice family below dyna.slice. The intermediate
// dyna-room.slice needs no unit of its own: systemd creates it with defaults.
const roomSlicePrefix = "dyna-room-"

// unitNameMax is systemd's unit name limit. A room id close to it keeps a
// deterministic short name, so the same room always maps to the same slice.
const unitNameMax = 255

// RoomSlice returns the slice unit that holds one room's agent work. The name
// nests below dyna-room.slice and dyna.slice, so the shared machine-wide caps
// still apply above it. A "-" in the room id would nest another level and a
// "/" cannot appear in a unit name, so everything outside [A-Za-z0-9_.] is
// escaped the way systemd-escape escapes it, as \xNN.
func RoomSlice(roomID string) string {
	escaped := escapeUnit(roomID)
	if prefix, rest := roomSlicePrefix, escaped+".slice"; len(prefix)+len(rest) > unitNameMax {
		sum := sha256.Sum256([]byte(roomID))
		escaped = escaped[:160] + "\\x2d" + hex.EncodeToString(sum[:6])
	}
	return roomSlicePrefix + escaped + ".slice"
}

// escapeUnit writes the unit-safe form of a room id: the characters a unit
// name allows stay, every other byte becomes \xNN, and a leading dot is
// escaped too because unit names cannot start with one.
func escapeUnit(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\x%02x", c)
		}
	}
	return b.String()
}

// RoomLimits are the kernel limits one room's work runs under. Zero values
// are left out of the unit file, so a caller can scale just some of them.
type RoomLimits struct {
	// MemoryHigh is where the kernel starts to throttle and reclaim the
	// room's processes instead of killing them.
	MemoryHigh int64
	// MemoryMax is the hard limit: the OOM killer takes a process inside the
	// room, never outside it.
	MemoryMax int64
	// TasksMax counts processes and threads; a fork above it fails inside the
	// room only.
	TasksMax int
}

// DefaultRoomLimits scales one room's share from the machine's memory:
// 40% throttles, 50% bounds the OOM kill, and the process cap grows with the
// machine from 2048 to 8192 tasks. dyna.slice still caps every room together
// above these.
func DefaultRoomLimits(memTotalKB uint64) RoomLimits {
	bytes := int64(memTotalKB) * 1024
	tasks := int(bytes/(1<<30)) * 256
	if tasks < 2048 {
		tasks = 2048
	}
	if tasks > 8192 {
		tasks = 8192
	}
	return RoomLimits{MemoryHigh: bytes * 40 / 100, MemoryMax: bytes / 2, TasksMax: tasks}
}

// RoomSliceUnit is the managed unit file of one room's slice. The description
// uses the escaped id, so a crafted room id cannot inject unit lines.
func RoomSliceUnit(roomID string, limits RoomLimits) string {
	var b strings.Builder
	b.WriteString(managedHeader + "\n[Unit]\nDescription=Dyna room " + escapeUnit(roomID) + " limits\n\n[Slice]")
	if limits.MemoryHigh > 0 {
		fmt.Fprintf(&b, "\nMemoryHigh=%d", limits.MemoryHigh)
	}
	if limits.MemoryMax > 0 {
		fmt.Fprintf(&b, "\nMemoryMax=%d", limits.MemoryMax)
	}
	if limits.TasksMax > 0 {
		fmt.Fprintf(&b, "\nTasksMax=%d", limits.TasksMax)
	}
	b.WriteString("\n")
	return b.String()
}

// WriteRoomSlice installs the managed room slice without contacting systemd.
// Like WriteSlice, it rewrites only a managed file whose content changed, so
// a local edit that removed the header is kept.
func WriteRoomSlice(dir, roomID string, limits RoomLimits) (bool, error) {
	path := filepath.Join(dir, RoomSlice(roomID))
	unit := RoomSliceUnit(roomID, limits)
	old, err := os.ReadFile(path)
	if err == nil && (bytes.Equal(old, []byte(unit)) || !bytes.HasPrefix(old, []byte(managedHeader))) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	if err := fsutil.WriteFile(path, []byte(unit), 0644, true); err != nil {
		return false, err
	}
	return true, nil
}

var (
	// roomSlices remembers which rooms this process already installed a slice
	// for, so a launch does not reread the configuration directory each time.
	roomSliceMu   sync.Mutex
	roomSliceDone = map[string]bool{}
)

// ensureRoom installs or refreshes the shared slice and this room's slice,
// and reloads the user manager once when either changed. Errors leave the
// slices as they are; systemd then runs the room in a slice with defaults.
func ensureRoom(roomID string) {
	roomSliceMu.Lock()
	done := roomSliceDone[roomID]
	roomSliceMu.Unlock()
	if done {
		return
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return
	}
	total, err := totalMemory()
	if err != nil {
		return
	}
	dir := filepath.Join(config, "systemd", "user")
	shared, err := WriteSlice(dir)
	if err != nil {
		return
	}
	room, err := WriteRoomSlice(dir, roomID, DefaultRoomLimits(total))
	if err != nil {
		return
	}
	if shared || room {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = runCommand(ctx, "systemctl", "--user", "daemon-reload")
	}
	roomSliceMu.Lock()
	roomSliceDone[roomID] = true
	roomSliceMu.Unlock()
}

// WrapRoom moves cmd into a new scope below this room's slice and returns the
// scope's unit name. The room's processes also learn the slice through
// DYNA_ROOM_SLICE, so scripts that start their own systemd units can join the
// same limits. An empty room, a machine without the delegated controllers, or
// a missing systemd user manager keeps today's behavior: the shared dyna.slice.
func WrapRoom(cmd *exec.Cmd, name, roomID string) string {
	if strings.EqualFold(os.Getenv("DYNA_SLICE"), "off") {
		return ""
	}
	if roomID == "" {
		return wrap(cmd, name, Slice)
	}
	if ok, reason := RoomSupport(); !ok {
		roomLimitsOff(roomID, reason)
		return wrap(cmd, name, Slice)
	}
	if _, ok := systemdRun(); !ok {
		roomLimitsOff(roomID, "the systemd user manager is not running")
		return ""
	}
	slice := RoomSlice(roomID)
	ensureRoom(roomID)
	unit := wrap(cmd, name, slice)
	if unit != "" {
		cmd.Env = append(cmd.Environ(), mode.EnvRoomSlice+"="+slice)
	}
	return unit
}

// RoomLimitsNotice, when set, hears once per process that this machine cannot
// run room slices. The engine installs it and posts one typed notice to the
// room; other binaries leave it nil and simply degrade.
var RoomLimitsNotice func(roomID, reason string)

var noticeOnce sync.Once

func roomLimitsOff(roomID, reason string) {
	if RoomLimitsNotice == nil {
		return
	}
	noticeOnce.Do(func() { RoomLimitsNotice(roomID, reason) })
}

var (
	// cgroupRoot, selfCgroupPath and memInfoPath are the files the support
	// check reads. Tests point them at fake roots.
	cgroupRoot     = "/sys/fs/cgroup"
	selfCgroupPath = "/proc/self/cgroup"
	memInfoPath    = "/proc/meminfo"

	supportOnce sync.Once
	supportOK   bool
	supportWhy  string

	memOnce  sync.Once
	memTotal uint64
	memErr   error
)

// RoomSupport reports whether this machine can run one slice per room: cgroup
// v2 with the memory and pids controllers delegated to this user, and a
// readable memory size to scale the limits from. The reason names the missing
// piece for the degrade notice.
func RoomSupport() (bool, string) {
	supportOnce.Do(func() {
		supportOK, supportWhy = checkRoomSupport()
	})
	return supportOK, supportWhy
}

func checkRoomSupport() (bool, string) {
	controllers, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.controllers"))
	if err != nil {
		// cgroup v1, or a container without cgroup v2 mounted.
		return false, "cgroup v2 with the memory and pids controllers is required"
	}
	have := map[string]bool{}
	for _, name := range strings.Fields(string(controllers)) {
		have[name] = true
	}
	if !have["memory"] || !have["pids"] {
		return false, "the cgroup has no " + missingController(have) + " controller"
	}
	userPath, ok := userManagerCgroup()
	if !ok {
		return false, "Dyna could not find the systemd user manager's cgroup"
	}
	delegated, err := os.ReadFile(filepath.Join(cgroupRoot, userPath, "cgroup.subtree_control"))
	if err != nil {
		return false, "systemd delegated no controllers to this user"
	}
	enabled := map[string]bool{}
	for _, name := range strings.Fields(string(delegated)) {
		enabled[strings.TrimPrefix(name, "+")] = true
	}
	if !enabled["memory"] || !enabled["pids"] {
		return false, "systemd did not delegate the " + missingController(enabled) + " controller to this user"
	}
	if _, err := totalMemory(); err != nil {
		return false, "Dyna could not read the machine's memory size"
	}
	return true, ""
}

// missingController names the controller a check lacks, for a reason that
// names what the machine is missing.
func missingController(have map[string]bool) string {
	if !have["memory"] {
		return "memory"
	}
	return "pids"
}

// userManagerCgroup returns the cgroup path of the systemd user manager this
// process runs under, whose subtree_control says which controllers the user
// may use. The innermost user@.service ancestor wins, so a user manager
// nested inside another reports its own delegation.
func userManagerCgroup() (string, bool) {
	raw, err := os.ReadFile(selfCgroupPath)
	if err != nil {
		return "", false
	}
	path := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			path = strings.TrimSpace(rest)
			break
		}
	}
	if path == "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if unit := parts[i]; strings.HasPrefix(unit, "user@") && strings.HasSuffix(unit, ".service") {
			return strings.Join(parts[:i+1], "/"), true
		}
	}
	return "", false
}

// totalMemory reads the machine's memory size once per process, in the kB
// that /proc/meminfo reports.
func totalMemory() (uint64, error) {
	memOnce.Do(func() {
		raw, err := os.ReadFile(memInfoPath)
		if err != nil {
			memErr = err
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[0] != "MemTotal:" || fields[2] != "kB" {
				continue
			}
			total, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || total == 0 {
				memErr = fmt.Errorf("meminfo MemTotal %q", fields[1])
				return
			}
			memTotal = total
			return
		}
		memErr = fmt.Errorf("meminfo has no MemTotal")
	})
	return memTotal, memErr
}
