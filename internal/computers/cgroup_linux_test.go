//go:build linux

package computers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cgroupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, value := range map[string]string{
		"cgroup.controllers": "cpuset cpu memory pids io", "cgroup.procs": "123\n", "cgroup.subtree_control": "",
		"daemon/cgroup.procs": "",
	} {
		putCgroupFile(t, root, name, value)
	}
	return root
}
func putCgroupFile(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func assertCgroupFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || strings.TrimSpace(string(data)) != want {
		t.Fatalf("%s: got %q, %v; want %q", name, data, err, want)
	}
}
func TestCgroupSetupAndLimits(t *testing.T) {
	root := cgroupFixture(t)
	var m cgroupManager
	if err := m.setup(root, 123, "3221225472"); err != nil {
		t.Fatal(err)
	}
	assertCgroupFile(t, root, "daemon/cgroup.procs", "123")
	assertCgroupFile(t, root, "cgroup.subtree_control", "+memory +cpu +pids")
	for _, id := range []string{"first", "second"} {
		dir := publicName(id)
		for _, file := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
			putCgroupFile(t, root, filepath.Join(dir, file), "")
		}
		if got := m.computer(id); got != filepath.Join(root, dir) {
			t.Fatalf("computer group = %q", got)
		}
		assertCgroupFile(t, root, filepath.Join(dir, "memory.high"), "3221225472")
		assertCgroupFile(t, root, filepath.Join(dir, "memory.oom.group"), "1")
		assertCgroupFile(t, root, filepath.Join(dir, "cpu.weight"), "100")
	}
}
func TestCgroupSetupFallback(t *testing.T) {
	for _, failure := range []string{"missing-controller", "shared-root", "move-denied", "enable-denied"} {
		t.Run(failure, func(t *testing.T) {
			root := cgroupFixture(t)
			switch failure {
			case "missing-controller":
				putCgroupFile(t, root, "cgroup.controllers", "cpu memory")
			case "shared-root":
				putCgroupFile(t, root, "cgroup.procs", "123\n456\n")
			case "move-denied":
				if err := os.Remove(filepath.Join(root, "daemon/cgroup.procs")); err != nil {
					t.Fatal(err)
				}
			case "enable-denied":
				if err := os.Remove(filepath.Join(root, "cgroup.subtree_control")); err != nil {
					t.Fatal(err)
				}
			}
			m := cgroupManager{root: "stale"}
			if err := m.setup(root, 123, ""); err == nil {
				t.Fatal("expected setup failure")
			}
			if m.computer("first") != "" {
				t.Fatal("failed setup enabled groups")
			}
			if failure == "missing-controller" || failure == "shared-root" {
				assertCgroupFile(t, root, "daemon/cgroup.procs", "")
			}
		})
	}
}
func TestCgroupMissingLimitFallback(t *testing.T) {
	for _, missing := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
		t.Run(missing, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
				if file != missing {
					putCgroupFile(t, root, filepath.Join(publicName("first"), file), "")
				}
			}
			m := cgroupManager{root: root, memoryHigh: defaultComputerMemoryHigh}
			if m.computer("first") != "" {
				t.Fatal("partial controls accepted")
			}
			if _, err := os.Stat(filepath.Join(root, publicName("first"), missing)); !os.IsNotExist(err) {
				t.Fatalf("created missing control: %v", err)
			}
		})
	}
	if got := (&cgroupManager{}).computer("first"); got != "" {
		t.Fatal(got)
	}
}
func TestCgroupMemoryHigh(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "max", "2GiB", "18446744073709551616"} {
		if got := computerMemoryHigh(value); got != defaultComputerMemoryHigh {
			t.Fatalf("%q: %s", value, got)
		}
	}
	if got := computerMemoryHigh("1073741824"); got != "1073741824" {
		t.Fatal(got)
	}
}
func TestCgroupPath(t *testing.T) {
	for input, want := range map[string]string{"0::/user.slice/dyna-computers.service\n": "/user.slice/dyna-computers.service", "2:cpu:/legacy\n": "", "0::/a/../b": "", "0::relative": ""} {
		if got := cgroupPath(input); got != want {
			t.Fatalf("%q: %q", input, got)
		}
	}
}
func TestCgroupBrowserPeerRoot(t *testing.T) {
	all := map[int]procInfo{10: {ppid: 1}, 20: {ppid: 10}, 30: {ppid: 20}, 40: {ppid: 30}}
	argv := map[int][]string{10: {"agent"}, 20: {"/opt/chromium/chrome", "--user-data-dir=/private"}, 30: {"/opt/chromium/chrome", "--type=zygote"}, 40: {"/opt/chromium/chrome", "--type=gpu-process"}}
	args := func(pid int) []string { return argv[pid] }
	for _, pid := range []int{20, 30, 40} {
		if got := browserPeerRoot(pid, all, args); got != 20 {
			t.Fatalf("peer %d -> %d", pid, got)
		}
	}
	for _, pid := range []int{20, 30, 40} {
		argv[pid] = []string{strings.Join(argv[pid], " ")}
	}
	if got := browserPeerRoot(40, all, args); got != 20 {
		t.Fatalf("rewritten Chromium title -> %d", got)
	}
	if got := browserPeerRoot(10, all, args); got != 10 {
		t.Fatal(got)
	}
	delete(argv, 20)
	if got := browserPeerRoot(40, all, args); got != 40 {
		t.Fatal("unverified ancestor accepted")
	}
	argv[20] = []string{"chrome", "--type=zygote"}
	all[20] = procInfo{ppid: 40}
	if got := browserPeerRoot(40, all, args); got != 40 {
		t.Fatal("cycle accepted")
	}
}

// Run only in a new, bounded, delegated systemd service with no other process.
func TestCgroupDelegatedIntegration(t *testing.T) {
	if os.Getenv("DYNA_TEST_CGROUP_DELEGATION") != "1" {
		t.Skip("requires isolated delegated service")
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	path := cgroupPath(string(data))
	if !strings.HasPrefix(filepath.Base(path), "dyna-cgroup-test-") {
		t.Fatal("refusing non-test cgroup")
	}
	root := filepath.Join("/sys/fs/cgroup", path)
	outer := map[string]string{}
	for _, file := range []string{"memory.high", "memory.max", "cpu.weight"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		outer[file] = strings.TrimSpace(string(data))
	}
	var m cgroupManager
	if err := m.setup(root, os.Getpid(), ""); err != nil {
		t.Fatal(err)
	}
	dir := m.computer("integration")
	if dir == "" {
		t.Fatal("computer group unavailable")
	}
	defer os.Remove(dir)
	assertCgroupFile(t, dir, "memory.high", defaultComputerMemoryHigh)
	assertCgroupFile(t, dir, "memory.oom.group", "1")
	assertCgroupFile(t, dir, "cpu.weight", "100")
	assertCgroupFile(t, root, "cgroup.procs", "")

	for file, value := range outer {
		assertCgroupFile(t, root, file, value)
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	s := supervisor{cgroup: dir}
	s.attachCgroup(child.Process.Pid)
	moved, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", child.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if got := cgroupPath(string(moved)); got != filepath.Join(path, publicName("integration")) {
		t.Fatalf("child group = %q", got)
	}
	assertCgroupFile(t, dir, "pids.current", "1")
	for _, file := range []string{"memory.current", "pids.current", "cpu.stat"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCgroupMemoryCurrent(t *testing.T) {
	root := t.TempDir()
	if _, ok := cgroupMemoryKiB(root); ok {
		t.Fatal("missing memory accepted")
	}
	if _, ok := cgroupMemoryKiB(""); ok {
		t.Fatal("empty group accepted")
	}
	for value, want := range map[string]int64{"0\n": 0, "2098175\n": 2048} {
		putCgroupFile(t, root, "memory.current", value)
		if got, ok := cgroupMemoryKiB(root); !ok || got != want {
			t.Fatalf("%q: %d, %v", value, got, ok)
		}
	}
	for _, value := range []string{"-1", "max", "oops", "9223372036854775808"} {
		putCgroupFile(t, root, "memory.current", value)
		if _, ok := cgroupMemoryKiB(root); ok {
			t.Fatalf("invalid memory %q accepted", value)
		}
	}
}
