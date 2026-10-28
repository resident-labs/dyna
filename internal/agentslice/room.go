package agentslice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
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

// RoomIDFromSlice returns the room id whose slice has this name, so a reader
// of the cgroup tree maps each room slice back to its room. The bool is false
// when the name is no room slice: a wrong prefix or suffix, an empty id, a
// malformed escape, or bytes that do not round-trip through RoomSlice.
func RoomIDFromSlice(slice string) (string, bool) {
	rest, ok := strings.CutPrefix(slice, roomSlicePrefix)
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ".slice")
	if !ok || rest == "" {
		return "", false
	}
	id, ok := unescapeUnit(rest)
	if !ok || RoomSlice(id) != slice {
		return "", false
	}
	return id, true
}

// RoomID is a canonical room identity decoded from Dyna's launch layout.
type RoomID string

// RoomIDFromCgroup returns the room id that owns a unified cgroup v2 path.
// Only the canonical room slice below dyna.slice establishes ownership; a
// room-shaped unit elsewhere, a relative or non-clean path, and a malformed
// slice name establish none.
func RoomIDFromCgroup(cgroup string) (RoomID, bool) {
	if cgroup == "" || cgroup[0] != '/' || path.Clean(cgroup) != cgroup {
		return "", false
	}
	units := strings.Split(strings.TrimPrefix(cgroup, "/"), "/")
	for i := 0; i+2 < len(units); i++ {
		if units[i] == Slice && units[i+1] == "dyna-room.slice" {
			id, ok := RoomIDFromSlice(units[i+2])
			return RoomID(id), ok
		}
	}
	return "", false
}

// unescapeUnit reverses escapeUnit: every \xNN becomes its byte again. It
// reports false on a malformed escape, such as a lone backslash or a hex
// digit that is not one.
func unescapeUnit(escaped string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(escaped); {
		if escaped[i] != '\\' {
			b.WriteByte(escaped[i])
			i++
			continue
		}
		if i+4 > len(escaped) || escaped[i+1] != 'x' {
			return "", false
		}
		v, err := strconv.ParseUint(escaped[i+2:i+4], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(v))
		i += 4
	}
	return b.String(), true
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

// RoomLimitOverride is one limit of a room's saved setting: the zero value
// keeps the machine default, Off removes the limit, and a positive Value
// replaces the default (bytes for memory, a count for tasks).
type RoomLimitOverride struct {
	Off   bool
	Value int64
}

// RoomLimitsSetting is a room's saved slice limits, as the engine reports
// them from the room record.
type RoomLimitsSetting struct {
	MemoryHigh RoomLimitOverride
	MemoryMax  RoomLimitOverride
	TasksMax   RoomLimitOverride
}

// Apply returns base with this setting's overrides in place: a set value
// replaces the default, off removes the limit, and default keeps it.
func (s RoomLimitsSetting) Apply(base RoomLimits) RoomLimits {
	if v := s.MemoryHigh; v.Off {
		base.MemoryHigh = 0
	} else if v.Value > 0 {
		base.MemoryHigh = v.Value
	}
	if v := s.MemoryMax; v.Off {
		base.MemoryMax = 0
	} else if v.Value > 0 {
		base.MemoryMax = v.Value
	}
	if v := s.TasksMax; v.Off {
		base.TasksMax = 0
	} else if v.Value > 0 {
		base.TasksMax = int(v.Value)
	}
	return base
}

// RoomLimitsLookup, when set, returns one room's saved limit setting. The
// engine installs it, reading the room record; other binaries leave it nil
// and every room runs with the machine defaults.
var RoomLimitsLookup func(roomID string) (RoomLimitsSetting, bool)

// roomLimits resolves the limits one room runs under: its saved setting over
// the machine defaults.
func roomLimits(roomID string, total uint64) RoomLimits {
	limits := DefaultRoomLimits(total)
	if RoomLimitsLookup == nil {
		return limits
	}
	over, ok := RoomLimitsLookup(roomID)
	if !ok {
		return limits
	}
	return over.Apply(limits)
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

	// roomLocks holds one mutex per room, so a settings change and a launch
	// that both write the same room's slice cannot interleave: an older
	// change would otherwise move the live slice back after a newer one.
	roomLocksMu sync.Mutex
	roomLocks   = map[string]*sync.Mutex{}
)

// roomMutex returns the room's slice mutex, creating it once.
func roomMutex(roomID string) *sync.Mutex {
	roomLocksMu.Lock()
	defer roomLocksMu.Unlock()
	mu := roomLocks[roomID]
	if mu == nil {
		mu = &sync.Mutex{}
		roomLocks[roomID] = mu
	}
	return mu
}

// lockRoom holds this room's slice work and returns what releases it. The
// caller reads the room's setting only under it, so an apply always sees the
// newest saved limits and lands after every earlier apply.
func lockRoom(roomID string) func() {
	mu := roomMutex(roomID)
	mu.Lock()
	return mu.Unlock
}

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
	unlock := lockRoom(roomID)
	room, err := WriteRoomSlice(dir, roomID, roomLimits(roomID, total))
	if err == nil && (shared || room) {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = runCommand(ctx, "systemctl", "--user", "daemon-reload")
	}
	unlock()
	if err != nil {
		return
	}
	roomSliceMu.Lock()
	roomSliceDone[roomID] = true
	roomSliceMu.Unlock()
}

// ApplyRoomLimits applies one room's limits now, under the room's slice
// lock: it reads the saved setting, rewrites the managed slice file, and on
// a machine that can run room slices moves the live slice with systemctl
// set-property --runtime; an off limit becomes infinity, so a live slice
// loses a removed limit too. A machine without the user manager or the
// delegated controllers keeps the file only: the room already heard why it
// has no slice of its own.
func ApplyRoomLimits(roomID string) error {
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	total, err := totalMemory()
	if err != nil {
		return err
	}
	dir := filepath.Join(config, "systemd", "user")
	unlock := lockRoom(roomID)
	defer unlock()
	limits := roomLimits(roomID, total)
	changed, err := WriteRoomSlice(dir, roomID, limits)
	if err != nil {
		return err
	}
	if _, ok := systemdRun(); !ok {
		return nil
	}
	if changed {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = runCommand(ctx, "systemctl", "--user", "daemon-reload")
	}
	if ok, _ := RoomSupport(); !ok {
		return nil
	}
	props := []string{
		"MemoryHigh=" + memoryLimit(limits.MemoryHigh),
		"MemoryMax=" + memoryLimit(limits.MemoryMax),
		"TasksMax=" + memoryLimit(int64(limits.TasksMax)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	args := append([]string{"--user", "set-property", "--runtime", RoomSlice(roomID)}, props...)
	return runCommand(ctx, "systemctl", args...)
}

// memoryLimit writes one limit the way systemctl set-property reads it: a
// count, or infinity when the limit is off.
func memoryLimit(v int64) string {
	if v > 0 {
		return strconv.FormatInt(v, 10)
	}
	return "infinity"
}

// WrapRoom moves cmd into a new scope below this room's slice and returns the
// scope's unit name. The room's processes also learn their slice through
// DYNA_ROOM_SLICE, so scripts that start their own systemd units join the
// same limits; a degraded machine names the shared dyna.slice there. An
// empty room, or a machine without the delegated controllers, keeps today's
// behavior: the shared dyna.slice. Without a systemd user manager nothing
// wraps at all. properties name extra systemd unit properties, such as
// MemoryHigh=..., for a caller that keeps a long-running cgroup in the scope.
func WrapRoom(cmd *exec.Cmd, name, roomID string, properties ...string) string {
	if strings.EqualFold(os.Getenv("DYNA_SLICE"), "off") {
		return ""
	}
	if roomID == "" {
		return wrap(cmd, name, Slice)
	}
	if ok, reason := RoomSupport(); !ok {
		roomLimitsOff(roomID, reason)
		unit := wrap(cmd, name, Slice)
		// The room shares the agent slice, and its scripts must hear that:
		// the guide's systemd-run daemon command joins whatever this variable
		// names, and without it such a daemon would leave the shared caps too.
		cmd.Env = append(cmd.Environ(), mode.EnvRoomSlice+"="+Slice)
		return unit
	}
	if _, ok := systemdRun(); !ok {
		roomLimitsOff(roomID, "the systemd user manager is not running")
		return ""
	}
	slice := RoomSlice(roomID)
	ensureRoom(roomID)
	unit := wrap(cmd, name, slice, properties...)
	if unit != "" {
		cmd.Env = append(cmd.Environ(), mode.EnvRoomSlice+"="+slice)
	}
	return unit
}

// RoomLimitsNotice, when set, hears that this machine cannot run room
// slices, once per room and cause in this process; the posted event's own
// Once rule keeps one card per room and cause across restarts. The engine
// installs it; other binaries leave it nil and simply degrade.
var RoomLimitsNotice func(roomID, reason string)

var (
	roomNoticeMu sync.Mutex
	roomNoticed  = map[string]bool{}
)

func roomLimitsOff(roomID, reason string) {
	if RoomLimitsNotice == nil {
		return
	}
	// One callback per room and cause: a second room hears its own notice, and
	// a machine whose cause changed hears the new one.
	key := roomID + "\x00" + reason
	roomNoticeMu.Lock()
	if roomNoticed[key] {
		roomNoticeMu.Unlock()
		return
	}
	roomNoticed[key] = true
	roomNoticeMu.Unlock()
	RoomLimitsNotice(roomID, reason)
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
	userPath, ok := userManagerCgroupAt(selfCgroupPath)
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

// userManagerCgroupAt returns the cgroup path of the systemd user manager
// that the given /proc/<pid>/cgroup file names, whose subtree_control says
// which controllers the user may use. The innermost user@.service ancestor
// wins, so a user manager nested inside another reports its own delegation.
func userManagerCgroupAt(selfCgroup string) (string, bool) {
	raw, err := os.ReadFile(selfCgroup)
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

// RoomPids counts the processes of every room's slice on this machine: what
// a room's own supervisors started under dyna-room-*.slice, which no Dyna
// session scope holds and so no turn count sees. A machine without cgroup
// v2, a user manager, or any room slice counts zero: callers degrade
// quietly.
func RoomPids() (byRoom map[string]int, total int) {
	return RoomPidsAt(cgroupRoot, selfCgroupPath)
}

// RoomPidsAt counts the room slices of the cgroup v2 tree at root, keyed by
// slice unit, and their sum, processes and threads together as pids.current
// reports them. selfCgroup names the /proc/self/cgroup file that says which
// user manager to look below; both paths are parameters so a caller can
// inject a fake tree. Without a readable root, a user manager, or any room
// slice it returns zero: callers degrade quietly.
func RoomPidsAt(root, selfCgroup string) (byRoom map[string]int, total int) {
	userPath, ok := userManagerCgroupAt(selfCgroup)
	if !ok {
		return nil, 0
	}
	// A room slice's name nests it below dyna-room.slice below the shared
	// dyna.slice, so every room's cgroup sits in one directory.
	dir := filepath.Join(root, userPath, Slice, "dyna-room.slice")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0 // no cgroup v2 root, or no room has a slice yet
	}
	byRoom = map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, roomSlicePrefix) || !strings.HasSuffix(name, ".slice") {
			continue
		}
		// A slice can vanish between the listing and the read, when its last
		// process exits, so a room that cannot be read is skipped.
		raw, err := os.ReadFile(filepath.Join(dir, name, "pids.current"))
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			continue
		}
		byRoom[name] = n
		total += n
	}
	return byRoom, total
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
