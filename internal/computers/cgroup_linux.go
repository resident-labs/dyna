//go:build linux

package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
