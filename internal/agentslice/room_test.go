package agentslice

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/mode"
)

// fakeCgroups writes a cgroup v2 root whose user manager has the memory and
// pids controllers delegated, and a meminfo of total kB. It returns the root.
func fakeCgroups(t *testing.T, controllers, delegated string, totalKB uint64) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, text string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("cgroup.controllers", controllers)
	write("user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control", delegated)
	self := t.TempDir()
	if err := os.WriteFile(filepath.Join(self, "self"), []byte("0::/user.slice/user-1000.slice/user@1000.service/dyna.slice/dyna-ses_1.scope\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(self, "meminfo"), []byte(fmt.Sprintf("MemTotal:       %d kB\n", totalKB)), 0644); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldSelf, oldMem := cgroupRoot, selfCgroupPath, memInfoPath
	cgroupRoot, selfCgroupPath, memInfoPath = root, filepath.Join(self, "self"), filepath.Join(self, "meminfo")
	supportOnce, memOnce = sync.Once{}, sync.Once{}
	t.Cleanup(func() {
		cgroupRoot, selfCgroupPath, memInfoPath = oldRoot, oldSelf, oldMem
		supportOnce, memOnce = sync.Once{}, sync.Once{}
	})
	return root
}

// A room id reaches the slice name escaped, because "-" would nest a level in
// systemd and "/" cannot appear in a unit name at all.
func TestRoomSliceEscapesTheRoomID(t *testing.T) {
	for _, tc := range []struct{ room, want string }{
		{"roomlimits", "dyna-room-roomlimits.slice"},
		{"room.limits_2", "dyna-room-room.limits_2.slice"},
		{"room-limits", `dyna-room-room\x2dlimits.slice`},
		{"a/b", `dyna-room-a\x2fb.slice`},
		{".hidden", `dyna-room-\x2ehidden.slice`},
		{"spä ce", `dyna-room-sp\xc3\xa4\x20ce.slice`},
	} {
		if got := RoomSlice(tc.room); got != tc.want {
			t.Errorf("RoomSlice(%q) = %q, want %q", tc.room, got, tc.want)
		}
	}
}

// A room id longer than a unit name allows keeps a deterministic name, so the
// same room always maps to the same slice and two rooms stay apart.
func TestRoomSliceStaysDeterministicForLongIDs(t *testing.T) {
	long := strings.Repeat("z", 400)
	first, repeat := RoomSlice(long), RoomSlice(long)
	second := RoomSlice(long + "-different-tail")
	if first != repeat {
		t.Fatalf("the same long room id mapped to two slices: %q vs %q", first, repeat)
	}
	if first == second {
		t.Fatalf("two long room ids collided into one slice: %q", first)
	}
	if len(first) > 255 {
		t.Fatalf("slice name %q is %d bytes, systemd allows 255", first, len(first))
	}
	if !strings.HasPrefix(first, "dyna-room-") || !strings.HasSuffix(first, ".slice") {
		t.Fatalf("slice name %q lost its room form", first)
	}
}

// The room's share scales with the machine's memory: a small machine still
// gets a working process cap, and a large one stops at 8192 tasks.
func TestDefaultRoomLimitsScaleWithMemory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		totalKB uint64
		high    int64
		max     int64
		tasks   int
	}{
		{"4 GiB", 4194304, 1717986918, 2147483648, 2048},
		{"32 GiB", 33554432, 13743895347, 17179869184, 8192},
		{"256 GiB", 268435456, 109951162777, 137438953472, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultRoomLimits(tc.totalKB)
			if got.MemoryHigh != tc.high || got.MemoryMax != tc.max || got.TasksMax != tc.tasks {
				t.Fatalf("limits = %+v, want high %d max %d tasks %d", got, tc.high, tc.max, tc.tasks)
			}
		})
	}
}

// The room unit file is managed: rewritten when its content changed, left
// alone when a local edit removed the managed header.
func TestWriteRoomSliceManagedHeaderRule(t *testing.T) {
	dir := t.TempDir()
	limits := RoomLimits{MemoryHigh: 1 << 30, MemoryMax: 2 << 30, TasksMax: 2048}
	path := filepath.Join(dir, RoomSlice("room-limits"))

	changed, err := WriteRoomSlice(dir, "room-limits", limits)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%t err=%v", changed, err)
	}
	changed, err = WriteRoomSlice(dir, "room-limits", limits)
	if err != nil || changed {
		t.Fatalf("unchanged write: changed=%t err=%v", changed, err)
	}
	if got, _ := os.ReadFile(path); string(got) != RoomSliceUnit("room-limits", limits) {
		t.Fatalf("room unit = %q, want %q", got, RoomSliceUnit("room-limits", limits))
	}

	if err := os.WriteFile(path, []byte("[Slice]\nCPUWeight=9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err = WriteRoomSlice(dir, "room-limits", limits); err != nil || changed {
		t.Fatalf("local edit rewritten: changed=%t err=%v", changed, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "[Slice]\nCPUWeight=9\n" {
		t.Fatalf("local edit changed: %q", got)
	}

	if err := os.WriteFile(path, []byte(managedHeader+"\n[Slice]\nCPUWeight=9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if changed, err = WriteRoomSlice(dir, "room-limits", limits); err != nil || !changed {
		t.Fatalf("stale managed unit not refreshed: changed=%t err=%v", changed, err)
	}
}

// With room limits available, a room launch lands in its own slice and tells
// its scripts which slice they run in; a launch without a room keeps the
// shared agent slice and exports nothing.
func TestWrapRoomUsesTheRoomSlice(t *testing.T) {
	// A test run inside a room slice inherits DYNA_ROOM_SLICE; the roomless
	// launch below must start from a clean environment.
	t.Setenv(mode.EnvRoomSlice, "")
	config, calls := fakeManager(t)
	fakeCgroups(t, "cpuset cpu io memory pids\n", "cpu memory pids\n", 33554432)

	cmd := exec.Command("/bin/dyna", "subagent", "_turn", "ses_1", "2")
	scope := WrapRoom(cmd, "ses_1/2", "room-limits")
	args := strings.Join(cmd.Args, " ")
	if scope == "" {
		t.Fatal("WrapRoom did not wrap")
	}
	if want := "--slice=" + RoomSlice("room-limits"); !strings.Contains(args, want) {
		t.Fatalf("args %q missing %q", args, want)
	}
	if got := envOf(cmd, mode.EnvRoomSlice); got != RoomSlice("room-limits") {
		t.Fatalf("%s = %q, want %q", mode.EnvRoomSlice, got, RoomSlice("room-limits"))
	}
	// The room slice and the shared slice above it are both installed, and
	// one reload covers them.
	for _, unit := range []string{Slice, RoomSlice("room-limits")} {
		if _, err := os.Stat(filepath.Join(config, "systemd", "user", unit)); err != nil {
			t.Errorf("unit %s missing: %v", unit, err)
		}
	}
	if len(*calls) != 1 || (*calls)[0] != "systemctl --user daemon-reload" {
		t.Fatalf("calls = %q", *calls)
	}

	plain := exec.Command("/bin/true")
	WrapRoom(plain, "x", "")
	plainArgs := strings.Join(plain.Args, " ")
	if !strings.Contains(plainArgs, "--slice="+Slice) {
		t.Fatalf("roomless launch args %q missing the shared slice", plainArgs)
	}
	if got := envOf(plain, mode.EnvRoomSlice); got != "" {
		t.Fatalf("roomless launch exports %s=%q", mode.EnvRoomSlice, got)
	}
}

// Without the delegated controllers, room launches stay in the shared slice,
// still tell their scripts which slice that is, and one notice per room says
// why: a second room hears its own notice, and a repeat launch stays quiet.
// A caller that keeps a long-running cgroup in the scope, such as the
// computers daemon's per-computer scope, passes its resource properties
// through to the unit systemd starts.
func TestWrapRoomPassesScopeProperties(t *testing.T) {
	fakeManager(t)
	fakeCgroups(t, "cpuset cpu io memory pids\n", "cpu memory pids\n", 33554432)
	cmd := exec.Command("/bin/true")
	if WrapRoom(cmd, "computer-x", "room-limits", "MemoryHigh=2147483648", "CPUWeight=100") == "" {
		t.Fatal("WrapRoom did not wrap")
	}
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--property=MemoryHigh=2147483648", "--property=CPUWeight=100"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
}

func TestWrapRoomDegradesWithoutControllers(t *testing.T) {
	fakeManager(t)
	root := fakeCgroups(t, "cpuset cpu io\n", "", 33554432) // no memory, no pids
	if err := os.Remove(filepath.Join(root, "user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control")); err != nil {
		t.Fatal(err)
	}

	var noticed []string
	old := RoomLimitsNotice
	RoomLimitsNotice = func(roomID, reason string) { noticed = append(noticed, roomID+" "+reason) }
	t.Cleanup(func() { RoomLimitsNotice = old })

	for i, room := range []string{"room-limits", "other", "room-limits"} {
		cmd := exec.Command("/bin/true")
		WrapRoom(cmd, "x", room)
		if args := strings.Join(cmd.Args, " "); !strings.Contains(args, "--slice="+Slice) {
			t.Fatalf("degraded launch for %q left the shared slice: %q", room, args)
		}
		// The room's scripts still land their daemons in the shared slice: the
		// guide's systemd-run command relies on this variable.
		if got := envOf(cmd, mode.EnvRoomSlice); got != Slice {
			t.Fatalf("degraded launch %d exports %s=%q, want %q", i, mode.EnvRoomSlice, got, Slice)
		}
	}
	if len(noticed) != 2 || !strings.Contains(noticed[0], "room-limits ") || !strings.Contains(noticed[1], "other ") {
		t.Fatalf("notices = %q, want one each for room-limits and other, and none for the repeat", noticed)
	}
	for _, note := range noticed {
		if !strings.Contains(note, "memory") {
			t.Fatalf("notice %q does not name the missing memory controller", note)
		}
	}
}

// The support check reads the cgroup root it is given and says which piece of
// the delegation is missing.
func TestRoomSupportNamesTheMissingPiece(t *testing.T) {
	for _, tc := range []struct {
		name        string
		controllers string
		delegated   string
		wantOK      bool
		wantReason  string
	}{
		{"delegated", "cpuset cpu io memory pids\n", "cpu memory pids\n", true, ""},
		{"cgroup v1 has no controllers file", "", "cpu memory pids\n", false, "cgroup v2"},
		{"no pids controller", "cpuset cpu io memory\n", "cpu memory\n", false, "pids"},
		{"memory not delegated", "cpuset cpu io memory pids\n", "cpu pids\n", false, "delegat"},
		{"nothing delegated", "cpuset cpu io memory pids\n", "", false, "delegat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeCgroups(t, tc.controllers, tc.delegated, 33554432)
			if tc.controllers == "" {
				if err := os.Remove(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.delegated == "" {
				if err := os.Remove(filepath.Join(cgroupRoot, "user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control")); err != nil {
					t.Fatal(err)
				}
			}
			ok, reason := RoomSupport()
			if ok != tc.wantOK || (tc.wantReason != "" && !strings.Contains(reason, tc.wantReason)) {
				t.Fatalf("support = %t, %q; want ok %t with reason containing %q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

// A process outside a user manager cannot use room slices either.
func TestRoomSupportRefusesOutsideAUserManager(t *testing.T) {
	fakeCgroups(t, "memory pids\n", "memory pids\n", 33554432)
	self := t.TempDir()
	if err := os.WriteFile(filepath.Join(self, "self"), []byte("0::/machine.slice/machine-1.scope\n"), 0644); err != nil {
		t.Fatal(err)
	}
	selfCgroupPath = filepath.Join(self, "self")
	supportOnce = sync.Once{}
	t.Cleanup(func() { supportOnce = sync.Once{} })
	if ok, reason := RoomSupport(); ok || !strings.Contains(reason, "user manager") {
		t.Fatalf("support = %t, %q; want refused with the user manager named", ok, reason)
	}
}

// Outside a user manager there are no room slices to count, and counting
// still succeeds with zero.
func TestRoomPidsOutsideAUserManagerCountsZero(t *testing.T) {
	self := t.TempDir()
	if err := os.WriteFile(filepath.Join(self, "self"), []byte("0::/machine.slice/machine-1.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if byRoom, total := RoomPidsAt(t.TempDir(), filepath.Join(self, "self")); byRoom != nil || total != 0 {
		t.Fatalf("room pids = %v, %d; want none outside a user manager", byRoom, total)
	}
}

func envOf(cmd *exec.Cmd, key string) string {
	prefix := key + "="
	for _, entry := range cmd.Environ() {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
