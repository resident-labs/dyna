//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/shellquote"
)

// The service root holds no processes after setup. Its daemon leaf and
// computer leaves share CPU below the existing dyna.slice limits.
const daemonCgroupLeaf = "daemon"
const defaultComputerMemoryHigh = "2147483648"

var computerGroups cgroupManager

type cgroupManager struct {
	root       string
	memoryHigh string
}

// writeCgroup never creates a missing control file. A regular directory is
// not a delegated cgroup and must not look like a successful fallback.
func writeCgroup(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(value)
	return err
}

func cgroupPath(data string) string {
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "0::/") {
			path := strings.TrimPrefix(line, "0::")
			if filepath.Clean(path) != path {
				return ""
			}
			return path
		}
	}
	return ""
}

func computerMemoryHigh(value string) string {
	n, err := strconv.ParseUint(value, 10, 63)
	if err != nil || n == 0 {
		return defaultComputerMemoryHigh
	}
	return strconv.FormatUint(n, 10)
}

// setup must run before starting children. Only the dedicated service owns
// this hierarchy; direct CLI starts must not rearrange their caller's scope.
func (m *cgroupManager) setup(root string, pid int, high string) error {
	m.root = ""
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return err
	}
	available := " " + strings.Join(strings.Fields(string(controllers)), " ") + " "
	for _, controller := range []string{"memory", "cpu", "pids"} {
		if !strings.Contains(available, " "+controller+" ") {
			return fmt.Errorf("cgroup controller %s is unavailable", controller)
		}
	}
	// Refuse a shared service root. On restart the root is normally empty.
	procs, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		return err
	}
	for _, value := range strings.Fields(string(procs)) {
		if value != strconv.Itoa(pid) {
			return fmt.Errorf("cgroup root contains another process")
		}
	}
	leaf := filepath.Join(root, daemonCgroupLeaf)
	if err = os.Mkdir(leaf, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err = writeCgroup(filepath.Join(leaf, "cgroup.procs"), strconv.Itoa(pid)); err != nil {
		return err
	}
	if err = writeCgroup(filepath.Join(root, "cgroup.subtree_control"), "+memory +cpu +pids"); err != nil {
		return err
	}
	m.root, m.memoryHigh = root, computerMemoryHigh(high)
	return nil
}

func setupComputerCgroups() {
	// Tests use temporary hierarchies and never change the test runner's group.
	if testing.Testing() {
		return
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return
	}
	path := cgroupPath(string(data))
	unit := filepath.Base(path)
	if unit != "dyna-computers.service" && !(strings.HasPrefix(unit, "dyna-computers-") && strings.HasSuffix(unit, ".service")) {
		return
	}
	if err = computerGroups.setup(filepath.Join("/sys/fs/cgroup", path), os.Getpid(), os.Getenv("DYNA_COMPUTER_MEMORY_HIGH")); err != nil {
		fmt.Fprintf(os.Stderr, "computer resource controls unavailable; using tracked processes: %v\n", err)
	}
}

func (m *cgroupManager) computer(id string) string {
	if m.root == "" {
		return ""
	}
	dir := filepath.Join(m.root, publicName(id))
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return ""
	}
	for _, limit := range []struct{ file, value string }{
		{"memory.high", m.memoryHigh}, {"memory.oom.group", "1"}, {"cpu.weight", "100"},
	} {
		if writeCgroup(filepath.Join(dir, limit.file), limit.value) != nil {
			_ = os.Remove(dir)
			return ""
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); err != nil {
		_ = os.Remove(dir)
		return ""
	}
	return dir
}

// cgroupMemoryKiB includes descendants and shared memory charged to the group.
func cgroupMemoryKiB(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(path, "memory.current"))
	if err != nil {
		return 0, false
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || bytes < 0 {
		return 0, false
	}
	return bytes / 1024, true
}

func computerCgroup(id string) string { return computerGroups.computer(id) }

// roomSliceDir returns the cgroup directory of a room's slice. The daemon's
// own service sits directly below dyna.slice, so the room slices are its
// siblings; a daemon outside dyna.slice has no room slice to join.
func (m *cgroupManager) roomSliceDir(room string) string {
	if m.root == "" {
		return ""
	}
	shared := filepath.Dir(m.root)
	if filepath.Base(shared) != "dyna.slice" {
		return ""
	}
	return filepath.Join(shared, "dyna-room.slice", agentslice.RoomSlice(room))
}

// roomSliceSupport reports whether this machine can run room slices; tests
// replace it while production asks agentslice.
var roomSliceSupport = agentslice.RoomSupport

// roomSlicesOn reports whether the daemon may place computers below room
// slices: the owner's DYNA_SLICE=off keeps today's shared leaf, and so does a
// machine without the delegated controllers.
func roomSlicesOn() bool {
	if strings.EqualFold(os.Getenv("DYNA_SLICE"), "off") {
		return false
	}
	ok, _ := roomSliceSupport()
	return ok
}

// underCgroup reports whether a cgroup path lies inside dir. An empty dir
// contains nothing.
func underCgroup(path, dir string) bool {
	return dir != "" && strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// startRoomScope starts one computer's scope below its room's slice and
// returns the scope's cgroup. Production asks systemd to run an anchor
// process there; tests replace it with a fake scope directory.
var startRoomScope = systemdRoomScope

// systemdRoomScope asks systemd to start an anchor process in a scope below
// the room's slice and returns the scope's cgroup. systemd owns the scope,
// so its control files stay put whatever else the room runs, which a
// directory the daemon created inside systemd's slice could not promise:
// systemd takes the slice's subtree_control back whenever its own units no
// longer need it. The anchor holds the scope open for the computer's
// processes and reports the scope's cgroup through the record the browser
// shims read.
func systemdRoomScope(ctx context.Context, id, room string) string {
	root := computerGroups.roomSliceDir(room)
	if root == "" {
		return ""
	}
	record := filepath.Join(RuntimeDir(id), "cgroup")
	tmp := record + ".tmp"
	script := "p=$(grep '^0::' /proc/self/cgroup) && printf '%s\\n' \"/sys/fs/cgroup${p#0::}\" > " + shellquote.Quote(tmp) + " && mv " + shellquote.Quote(tmp) + " " + shellquote.Quote(record) + " && exec sleep infinity"
	cmd := exec.Command("/bin/sh", "-c", script)
	// systemd-run needs the daemon's XDG_RUNTIME_DIR to reach the user manager.
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{"DYNA_COMPUTER_PROCESS": id})
	log, err := os.OpenFile(filepath.Join(Dir(id), "scope.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return ""
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	unit := agentslice.WrapRoom(cmd, "computer-"+id, room, "MemoryHigh="+computerGroups.memoryHigh, "CPUWeight=100")
	if unit == "" {
		return ""
	}
	// The record from an earlier scope satisfies the wait, so remove it first.
	_ = os.Remove(record)
	p, err := startTracked(cmd)
	if err != nil {
		return ""
	}
	// A failure past the start leaves a live anchor behind, so stop the
	// scope the way agentslice stops its own.
	stop := func() { agentslice.Stop(unit) }
	if waitPath(ctx, record, p) != nil {
		stop()
		return ""
	}
	data, err := os.ReadFile(record)
	if err != nil {
		stop()
		return ""
	}
	scope := strings.TrimSpace(string(data))
	if !underCgroup(scope, root) {
		stop()
		return ""
	}
	return scope
}

// scopeControls asserts the per-computer controls on a room scope and proves
// the daemon may move the computer's processes into it.
func scopeControls(scope string) bool {
	for _, limit := range []struct{ file, value string }{
		{"memory.oom.group", "1"}, {"memory.high", computerGroups.memoryHigh},
	} {
		if writeCgroup(filepath.Join(scope, limit.file), limit.value) != nil {
			return false
		}
	}
	return true
}

// savedScope returns the cgroup a previous daemon life recorded, when that
// scope still exists below the room's slice and still holds its anchor: a
// scope systemd is tearing down reads populated 0 and must be replaced.
func (s *supervisor) savedScope(id, root string) string {
	data, err := os.ReadFile(filepath.Join(RuntimeDir(id), "cgroup"))
	if err != nil {
		return ""
	}
	scope := strings.TrimSpace(string(data))
	if !underCgroup(scope, root) {
		return ""
	}
	events, err := os.ReadFile(filepath.Join(scope, "cgroup.events"))
	if err != nil || !strings.Contains(string(events), "populated 1") {
		return ""
	}
	return scope
}

// roomScopeCgroup returns this computer's cgroup below its room's slice: a
// scope an earlier daemon life left is adopted, and otherwise systemd starts
// a new one. It returns "" when the room path is unavailable.
func (s *supervisor) roomScopeCgroup(ctx context.Context, id, room string) string {
	root := computerGroups.roomSliceDir(room)
	if root == "" {
		return ""
	}
	if scope := s.savedScope(id, root); scope != "" {
		if scopeControls(scope) {
			return scope
		}
		// Unusable but alive: kill it rather than leave its anchor sleeping.
		_ = writeCgroup(filepath.Join(scope, "cgroup.kill"), "1")
	}
	scope := startRoomScope(ctx, id, room)
	if scope != "" && underCgroup(scope, root) && scopeControls(scope) {
		return scope
	}
	return ""
}

// ensureCgroup gives the coming desktop its cgroup: a scope below the
// computer's room slice when room slices work here, else the shared service
// leaf that caps every computer of every room together.
func (s *supervisor) ensureCgroup(ctx context.Context) {
	s.mu.Lock()
	id, room := s.meta.SessionID, s.meta.Room
	s.mu.Unlock()
	cgroup, slice := "", ""
	if room != "" && roomSlicesOn() {
		if scope := s.roomScopeCgroup(ctx, id, room); scope != "" {
			cgroup, slice = scope, agentslice.RoomSlice(room)
		}
	}
	if cgroup == "" {
		cgroup = computerCgroup(id)
	}
	s.mu.Lock()
	old := s.cgroup
	s.cgroup, s.roomSlice = cgroup, slice
	s.mu.Unlock()
	s.recordCgroup(cgroup)
	// A fallback leaf from an earlier attempt is empty by now; drop it so a
	// later room scope does not accumulate leaves below the service.
	if old != "" && old != cgroup && underCgroup(old, computerGroups.root) {
		_ = os.Remove(old)
	}
}

// recordCgroup stores the computer's cgroup for the browser shims, which
// join it from outside the daemon.
func (s *supervisor) recordCgroup(cgroup string) {
	path := filepath.Join(RuntimeDir(s.meta.SessionID), "cgroup")
	if cgroup == "" {
		_ = os.Remove(path)
		return
	}
	_ = os.WriteFile(path, []byte(cgroup+"\n"), 0600)
}

// killSavedScope kills the room scope a previous daemon life recorded, which
// outlived that daemon's death signal and now holds nothing but its anchor.
func (s *supervisor) killSavedScope() {
	root := computerGroups.roomSliceDir(s.meta.Room)
	if root == "" {
		return
	}
	if scope := s.savedScope(s.meta.SessionID, root); scope != "" {
		_ = writeCgroup(filepath.Join(scope, "cgroup.kill"), "1")
	}
}

// computerPeerPID handles Chromium's GPU process opening the display socket.
// Stop at the browser root, so an agent or shell never joins its computer.
func computerPeerPID(pid int) int {
	return browserPeerRoot(pid, processes(), func(pid int) []string {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	})
}

func browserPeerRoot(pid int, all map[int]procInfo, args func(int) []string) int {
	original := pid
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		p, ok := all[pid]
		argv := args(pid)
		if len(argv) == 1 {
			argv = strings.Fields(argv[0])
		}
		if !ok || len(argv) == 0 {
			return original
		}
		switch filepath.Base(argv[0]) {
		case "chrome", "chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome-headless-shell":
		default:
			return original
		}
		child := false
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "--type=") || arg == "--type" {
				child = true
			}
		}
		if !child {
			return pid
		}
		pid = p.ppid
	}
	return original
}

func (s *supervisor) attachCgroup(pid int) {
	if s.cgroup == "" {
		return
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid)); err == nil {
		if path := cgroupPath(string(data)); path != "" && filepath.Join("/sys/fs/cgroup", path) == s.cgroup {
			return
		}
	}
	all := processes()
	p, ok := all[pid]
	if !ok {
		return
	}
	for child := range descendants(all, map[int]string{pid: p.start}) {
		if current, ok := processInfo(child); ok && current.start == all[child].start {
			_ = writeCgroup(filepath.Join(s.cgroup, "cgroup.procs"), strconv.Itoa(child))
		}
	}
}
func (s *supervisor) killCgroup() {
	if s.cgroup == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(s.cgroup, "cgroup.freeze"), []byte("1"), 0600)
	_ = os.WriteFile(filepath.Join(s.cgroup, "cgroup.kill"), []byte("1"), 0600)
	_ = os.WriteFile(filepath.Join(s.cgroup, "cgroup.freeze"), []byte("0"), 0600)
}
