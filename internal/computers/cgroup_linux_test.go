//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store"
	"dyna-agent/internal/testsafe"
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

// roomSliceFixture builds the cgroup world of a daemon below dyna.slice: the
// service root, and support and starter seams a test can replace. The room
// scope starter is a no-op until a test replaces it.
func roomSliceFixture(t *testing.T, room string) (serviceRoot, roomRoot string) {
	t.Helper()
	base := t.TempDir()
	serviceRoot = filepath.Join(base, "dyna.slice", "dyna-computers.service")
	for name, value := range map[string]string{
		"cgroup.controllers": "cpuset cpu memory pids io", "cgroup.procs": "123\n", "cgroup.subtree_control": "",
		"daemon/cgroup.procs": "",
	} {
		putCgroupFile(t, serviceRoot, name, value)
	}
	var m cgroupManager
	if err := m.setup(serviceRoot, 123, "3221225472"); err != nil {
		t.Fatal(err)
	}
	oldGroups, oldSupport, oldStart, oldMount := computerGroups, roomSliceSupport, startRoomScope, cgroupMount
	computerGroups, cgroupMount = m, base
	roomSliceSupport = func() (bool, string) { return true, "" }
	startRoomScope = func(context.Context, string, string) string { return "" }
	t.Cleanup(func() {
		computerGroups, roomSliceSupport, startRoomScope, cgroupMount = oldGroups, oldSupport, oldStart, oldMount
	})
	roomRoot = computerGroups.roomSliceDir(room)
	if roomRoot == "" {
		t.Fatal("the fixture daemon is not below dyna.slice")
	}
	return serviceRoot, roomRoot
}

// fakeScope fakes the scope systemd starts below a room's slice: a cgroup
// directory whose control files the properties would have set, alive through
// its anchor.
func fakeScope(t *testing.T, roomRoot, name string) string {
	t.Helper()
	scope := filepath.Join(roomRoot, name)
	for file, value := range map[string]string{"cgroup.events": "populated 1\nfrozen 0\n"} {
		putCgroupFile(t, scope, file, value)
	}
	for _, file := range []string{"memory.high", "memory.oom.group", "cgroup.kill", "cgroup.procs"} {
		putCgroupFile(t, scope, file, "")
	}
	return scope
}

func recordScope(id string) string { return filepath.Join(RuntimeDir(id), "cgroup") }

// A computer of a room runs in its own scope below that room's slice, so the
// room's limits bound its computers, and its apps tell their own systemd-run
// calls which slice to join.
func TestRoomComputerRunsBelowItsRoomSlice(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	_, roomRoot := roomSliceFixture(t, "roomlimits")
	scope := fakeScope(t, roomRoot, "dyna-computer-one.scope")
	startRoomScope = func(context.Context, string, string) string { return scope }
	id := testSessionID(t, "room")
	s, err := newComputer(t.Context(), id, "roomlimits", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	if s.cgroup != "" {
		t.Fatalf("a room computer took a cgroup before its desktop started: %q", s.cgroup)
	}
	s.ensureCgroup(t.Context())
	if s.cgroup != scope {
		t.Fatalf("room computer cgroup = %q, want the room scope %q", s.cgroup, scope)
	}
	assertCgroupFile(t, scope, "memory.oom.group", "1")
	assertCgroupFile(t, scope, "memory.high", "3221225472")
	if s.roomSlice != agentslice.RoomSlice("roomlimits") {
		t.Fatalf("room slice = %q, want %q", s.roomSlice, agentslice.RoomSlice("roomlimits"))
	}
	data, err := os.ReadFile(recordScope(id))
	if err != nil || strings.TrimSpace(string(data)) != scope {
		t.Fatalf("cgroup record = %q, %v; want %q", data, err, scope)
	}
	s.mu.Lock()
	env := s.desktopEnv()
	s.mu.Unlock()
	if got := envValue(env, mode.EnvRoomSlice); got != agentslice.RoomSlice("roomlimits") {
		t.Fatalf("app env %s = %q, want the room slice", mode.EnvRoomSlice, got)
	}
}

// A computer without a room, or a machine without delegated room slices,
// keeps the shared leaf below the computer service, as before.
func TestComputersWithoutRoomSlicesKeepTheServiceLeaf(t *testing.T) {
	for _, tc := range []struct {
		name    string
		room    string
		support bool
	}{
		{"roomless", "", true},
		{"unsupported", "roomlimits", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
			serviceRoot, roomRoot := roomSliceFixture(t, "roomlimits")
			if !tc.support {
				roomSliceSupport = func() (bool, string) { return false, "no controllers" }
			}
			started := false
			startRoomScope = func(context.Context, string, string) string {
				started = true
				return fakeScope(t, roomRoot, "never.scope")
			}
			id := testSessionID(t, tc.name)
			leaf := filepath.Join(serviceRoot, publicName(id))
			for _, file := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
				putCgroupFile(t, leaf, file, "")
			}
			s, err := newComputer(t.Context(), id, tc.room, SupervisorOptions{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.cleanup(false) })
			if s.cgroup != leaf {
				t.Fatalf("cgroup = %q, want the service leaf %q", s.cgroup, leaf)
			}
			assertCgroupFile(t, leaf, "memory.high", "3221225472")
			s.ensureCgroup(t.Context())
			if s.cgroup != leaf || started {
				t.Fatalf("ensure moved the computer: cgroup %q, scope started %v", s.cgroup, started)
			}
			if s.roomSlice != "" {
				t.Fatalf("room slice = %q without a room scope", s.roomSlice)
			}
			s.mu.Lock()
			env := s.desktopEnv()
			s.mu.Unlock()
			if got := envValue(env, mode.EnvRoomSlice); got != "" {
				t.Fatalf("app env exports %s=%q without a room scope", mode.EnvRoomSlice, got)
			}
		})
	}
}

// A scope a previous daemon life left below the room's slice is adopted
// instead of starting a second one, so a crashed daemon's computer keeps its
// cgroup; a dead or foreign record never is.
func TestRoomScopeIsAdoptedAcrossDaemonRestarts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	serviceRoot, roomRoot := roomSliceFixture(t, "roomlimits")
	id := testSessionID(t, "adopt")
	s, err := newComputer(t.Context(), id, "roomlimits", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	scope := fakeScope(t, roomRoot, "dyna-computer-old.scope")
	putCgroupFile(t, scope, "memory.high", "1")
	if err := os.WriteFile(recordScope(id), []byte(scope+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	started := false
	startRoomScope = func(context.Context, string, string) string {
		started = true
		return ""
	}
	s.ensureCgroup(t.Context())
	if s.cgroup != scope || started {
		t.Fatalf("live record not adopted: cgroup %q, started %v", s.cgroup, started)
	}
	assertCgroupFile(t, scope, "memory.high", "3221225472")

	// A scope that died since the record was written is replaced by a new one.
	fresh := fakeScope(t, roomRoot, "dyna-computer-fresh.scope")
	startRoomScope = func(context.Context, string, string) string { return fresh }
	forget := func() {
		s.mu.Lock()
		s.cgroup, s.roomSlice = "", ""
		s.mu.Unlock()
	}
	// A dying scope still has its directory until systemd removes it, but its
	// anchor is gone: populated 0, never adopted.
	putCgroupFile(t, scope, "cgroup.events", "populated 0\nfrozen 0\n")
	forget()
	s.ensureCgroup(t.Context())
	if s.cgroup != fresh {
		t.Fatalf("dying record not replaced: cgroup %q, want %q", s.cgroup, fresh)
	}

	// A record outside the room's slice, such as a fallback leaf, is never
	// adopted as a scope.
	leaf := filepath.Join(serviceRoot, publicName(id))
	for _, file := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
		putCgroupFile(t, leaf, file, "")
	}
	startRoomScope = func(context.Context, string, string) string { return "" }
	if err := os.WriteFile(recordScope(id), []byte(leaf+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	forget()
	s.ensureCgroup(t.Context())
	if s.cgroup != leaf {
		t.Fatalf("foreign record adopted: cgroup %q, want the service leaf %q", s.cgroup, leaf)
	}
}

// A scope that starts but cannot take the computer's controls is killed
// before the computer falls back to the service leaf, so its anchor does not
// linger with nothing to hold.
func TestRoomScopeWithoutControlsIsKilled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	serviceRoot, roomRoot := roomSliceFixture(t, "roomlimits")
	scope := fakeScope(t, roomRoot, "dyna-computer-bad.scope")
	if err := os.Remove(filepath.Join(scope, "memory.high")); err != nil {
		t.Fatal(err)
	}
	startRoomScope = func(context.Context, string, string) string { return scope }
	id := testSessionID(t, "badscope")
	leaf := filepath.Join(serviceRoot, publicName(id))
	for _, file := range []string{"memory.high", "memory.oom.group", "cpu.weight", "cgroup.kill"} {
		putCgroupFile(t, leaf, file, "")
	}
	s, err := newComputer(t.Context(), id, "roomlimits", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	s.ensureCgroup(t.Context())
	assertCgroupFile(t, scope, "cgroup.kill", "1")
	if s.cgroup != leaf || s.roomSlice != "" {
		t.Fatalf("cgroup %q, room slice %q; want the service leaf", s.cgroup, s.roomSlice)
	}
}

// Stopping a computer takes its room scope down. Nothing may join the dead
// scope afterwards: the record goes, and the next ensure asks for a new one.
func TestStopForgetsTheRoomScope(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	_, roomRoot := roomSliceFixture(t, "roomlimits")
	first := fakeScope(t, roomRoot, "dyna-computer-first.scope")
	second := fakeScope(t, roomRoot, "dyna-computer-second.scope")
	next := first
	startRoomScope = func(context.Context, string, string) string { return next }
	id := testSessionID(t, "stop")
	s, err := newComputer(t.Context(), id, "roomlimits", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	s.ensureCgroup(t.Context())
	s.mu.Lock()
	_ = s.stop()
	s.mu.Unlock()
	assertCgroupFile(t, first, "cgroup.kill", "1")
	if s.cgroup != "" || s.roomSlice != "" {
		t.Fatalf("stopped computer keeps cgroup %q, room slice %q", s.cgroup, s.roomSlice)
	}
	if _, err := os.Stat(recordScope(id)); !os.IsNotExist(err) {
		t.Fatalf("stopped computer keeps its cgroup record: %v", err)
	}
	putCgroupFile(t, first, "cgroup.events", "populated 0\n")
	next = second
	s.ensureCgroup(t.Context())
	if s.cgroup != second {
		t.Fatalf("restart cgroup = %q, want %q", s.cgroup, second)
	}
}

// Destroying a computer without its daemon still kills the recorded room
// scope, because no later cleanup can find the record once it is removed.
func TestOfflineDestroyKillsTheRecordedRoomScope(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	_, roomRoot := roomSliceFixture(t, "roomlimits")
	scope := fakeScope(t, roomRoot, "dyna-computer-live.scope")
	id := testSessionID(t, "offline")
	if err := os.MkdirAll(RuntimeDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(Dir(id), "meta.json"), Meta{SessionID: id, Room: "roomlimits"}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordScope(id), []byte(scope+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := destroyOffline(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	assertCgroupFile(t, scope, "cgroup.kill", "1")
	// A record that does not name a computer scope is never written to.
	other := filepath.Join(roomRoot, "other.scope")
	putCgroupFile(t, other, "cgroup.kill", "")
	if err := os.MkdirAll(RuntimeDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordScope(id), []byte(other+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	killRecordedScope(id)
	assertCgroupFile(t, other, "cgroup.kill", "")
}

// The compositor, and so the session bus and everything that bus starts, gets
// the computer's own room slice even when the daemon was started from another
// room's environment.
func TestCompositorGetsTheComputersRoomSlice(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	t.Setenv(mode.EnvRoomSlice, "dyna-room-foreign.slice")
	_, roomRoot := roomSliceFixture(t, "roomlimits")
	scope := fakeScope(t, roomRoot, "dyna-computer-env.scope")
	startRoomScope = func(context.Context, string, string) string { return scope }
	bin, out := t.TempDir(), filepath.Join(t.TempDir(), "slice")
	script := "#!/bin/sh\nprintenv DYNA_ROOM_SLICE > " + shellquote.Quote(out) + "\nexit 1\n"
	for name, body := range map[string]string{"labwc": script, "Xwayland": "#!/bin/sh\nexit 1\n", "setpriv": "#!/bin/sh\nexit 1\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	s, err := newComputer(t.Context(), testSessionID(t, "env"), "roomlimits", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup(false) })
	if err := s.start(); err == nil {
		t.Fatal("the fake compositor exits, so the start must fail")
	}
	data, err := os.ReadFile(out)
	if err != nil || strings.TrimSpace(string(data)) != agentslice.RoomSlice("roomlimits") {
		t.Fatalf("compositor %s = %q, %v; want the computer's room slice", mode.EnvRoomSlice, data, err)
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
