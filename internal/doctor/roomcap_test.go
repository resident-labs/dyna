package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"dyna-agent/internal/agentslice"
)

// fakeRoom writes one room slice below a user manager root, with the cap
// files the doctor reads. An empty string leaves a file out, "max" writes the
// kernel's word for no limit.
func fakeRoom(t *testing.T, root, roomID, current, high, max, pidsCurrent, pidsMax string) {
	t.Helper()
	slice := filepath.Join(root, "dyna.slice", "dyna-room.slice", agentslice.RoomSlice(roomID))
	if err := os.MkdirAll(slice, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": current, "memory.high": high, "memory.max": max,
		"pids.current": pidsCurrent, "pids.max": pidsMax,
	}
	for name, body := range files {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(slice, name), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Each room slice reads its caps: both memory limits against memory.current,
// the process limit against pids.current. A limit of "max" or a missing file
// caps nothing, and a directory that is no room slice is left out.
func TestRoomCapsReadsEveryRoomSlice(t *testing.T) {
	root := t.TempDir()
	fakeRoom(t, root, "room__1", "810", "900", "1000", "81", "100")
	fakeRoom(t, root, "room-limits", "5", "max", "50", "2", "") // no pids cap, memory.high uncapped
	if err := os.MkdirAll(filepath.Join(root, "dyna.slice", "dyna-room.slice", "dyna-room-not\\xzza.slice"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := RoomCaps(root)
	if len(got) != 2 {
		t.Fatalf("rooms = %+v, want the two readable rooms", got)
	}
	byRoom := map[string]RoomUse{}
	for _, use := range got {
		byRoom[use.RoomID] = use
	}
	first := byRoom["room__1"]
	if first.Slice != agentslice.RoomSlice("room__1") || len(first.Caps) != 3 {
		t.Fatalf("room__1 = %+v", first)
	}
	for i, want := range []CapUse{
		{Cap: CapMemoryHigh, Current: 810, Limit: 900},
		{Cap: CapMemoryMax, Current: 810, Limit: 1000},
		{Cap: CapPidsMax, Current: 81, Limit: 100},
	} {
		if first.Caps[i] != want {
			t.Errorf("cap %d = %+v, want %+v", i, first.Caps[i], want)
		}
	}
	if second := byRoom["room-limits"]; len(second.Caps) != 1 || second.Caps[0] != (CapUse{Cap: CapMemoryMax, Current: 5, Limit: 50}) {
		t.Fatalf("room-limits = %+v", second)
	}
	if rooms := RoomCaps(filepath.Join(root, "missing")); len(rooms) != 0 {
		t.Fatalf("a missing root read %+v", rooms)
	}
}

// Memory alerts discount clean file cache on the active and inactive LRU
// lists and reclaimable slab. Dirty, writeback, shared and non-LRU file memory
// stays charged, as does all memory when the kernel statistics are incomplete.
func TestRoomCapsMemoryDiscountsOnlyReclaimableCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		stat string
		want uint64
	}{
		{
			name: "reclaimable file and slab",
			stat: "active_file 200\ninactive_file 300\nfile_dirty 20\nfile_writeback 10\nshmem 30\nslab_reclaimable 300\n",
			want: 260,
		},
		{
			name: "non-reclaimable memory still counts",
			stat: "active_file 60\ninactive_file 40\nfile_dirty 0\nfile_writeback 0\nshmem 0\nslab_reclaimable 0\n",
			want: 900,
		},
		{
			name: "incomplete statistics keep the raw reading",
			stat: "active_file 200\ninactive_file 300\nfile_dirty 20\nfile_writeback 10\nslab_reclaimable 300\n",
			want: 1000,
		},
		{
			name: "inconsistent statistics cannot underflow usage",
			stat: "active_file 200\ninactive_file 300\nfile_dirty 20\nfile_writeback 10\nshmem 30\nslab_reclaimable 2000\n",
			want: 1000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fakeRoom(t, root, "room__1", "1000", "1100", "1200", "1", "100")
			slice := filepath.Join(root, "dyna.slice", "dyna-room.slice", agentslice.RoomSlice("room__1"))
			if err := os.WriteFile(filepath.Join(slice, "memory.stat"), []byte(tc.stat), 0o644); err != nil {
				t.Fatal(err)
			}
			got := RoomCaps(root)
			if len(got) != 1 || len(got[0].Caps) != 3 {
				t.Fatalf("rooms = %+v", got)
			}
			for _, cap := range got[0].Caps[:2] {
				if cap.Current != tc.want {
					t.Errorf("%s current = %d, want %d", cap.Cap, cap.Current, tc.want)
				}
			}
		})
	}
}

// fakeRoomProc writes one process the way /proc shows it, with its parent.
func fakeRoomProc(t *testing.T, root string, pid, ppid int, command string, pages uint64) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) S %d 1 1 0 -1 0 0 0 0 0 5 0 0 0 20 0 1 0 100 0 0", pid, command, ppid)
	for name, body := range map[string]string{"stat": stat, "statm": fmt.Sprintf("100 %d 0", pages)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The room's processes run in scopes below its slice, so the trees root at
// the processes whose parent is outside: each names a program the room
// started, with the memory and count of everything under it.
func TestTopTreesGroupsByRootAndRanksBothWays(t *testing.T) {
	root := t.TempDir()
	slice := filepath.Join(root, "dyna.slice", "dyna-room.slice", "dyna-room-room__1.slice")
	scope := filepath.Join(slice, "dyna-ses_x-00000001.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir string, pids ...int) {
		t.Helper()
		body := ""
		for _, pid := range pids {
			body += fmt.Sprintf("%d\n", pid)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(slice, 10, 13)
	write(scope, 20, 21, 11, 12)
	// 10 roots a tree of 10, 11 and 12; 13 stands alone; 20 and 21 are one tree.
	fakeRoomProc(t, root, 10, 1, "pi", 25)
	fakeRoomProc(t, root, 11, 10, "bash", 12)
	fakeRoomProc(t, root, 12, 10, "grep", 13)
	fakeRoomProc(t, root, 13, 1, "mcp", 10)
	fakeRoomProc(t, root, 20, 99, "node", 50)
	fakeRoomProc(t, root, 21, 20, "tsc", 1)
	page := uint64(os.Getpagesize())
	got := TopTrees(slice, root, 2)
	wantMemory := []CapTree{
		{Command: "node", PID: 20, Memory: 51 * page, Count: 2},
		{Command: "pi", PID: 10, Memory: 50 * page, Count: 3},
	}
	if len(got.ByMemory) != len(wantMemory) {
		t.Fatalf("by memory = %+v", got.ByMemory)
	}
	for i, want := range wantMemory {
		if got.ByMemory[i] != want {
			t.Errorf("by memory %d = %+v, want %+v", i, got.ByMemory[i], want)
		}
	}
	wantCount := []CapTree{
		{Command: "pi", PID: 10, Memory: 50 * page, Count: 3},
		{Command: "node", PID: 20, Memory: 51 * page, Count: 2},
	}
	if len(got.ByCount) != len(wantCount) {
		t.Fatalf("by count = %+v", got.ByCount)
	}
	for i, want := range wantCount {
		if got.ByCount[i] != want {
			t.Errorf("by count %d = %+v, want %+v", i, got.ByCount[i], want)
		}
	}
	if empty := TopTrees(filepath.Join(root, "missing"), root, 5); len(empty.ByMemory) != 0 || len(empty.ByCount) != 0 {
		t.Fatalf("a missing slice read %+v", empty)
	}
}
