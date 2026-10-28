package doctor

import (
	"io/fs"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/proc"
)

// A room's agent work runs in its own cgroup slice, with a memory and a
// process cap that agentslice installs. The doctor reads each slice's use of
// its caps, so the room hears before the kernel starts to throttle its memory
// or refuse its forks.

// Cap names one cap of a room's slice, after the cgroup file that holds the
// limit.
type Cap string

const (
	// CapMemoryHigh is where the kernel starts to throttle and reclaim the
	// room's memory.
	CapMemoryHigh Cap = "memory.high"
	// CapMemoryMax is the hard memory limit: the OOM killer takes a process
	// inside the room, never outside it.
	CapMemoryMax Cap = "memory.max"
	// CapPidsMax bounds the room's processes: a fork above it fails inside the
	// room only.
	CapPidsMax Cap = "pids.max"
)

// CapUse is one room slice's use of one cap.
type CapUse struct {
	Cap Cap
	// Current is the use that the kernel cannot simply reclaim for memory
	// caps, and the process count for pids.max.
	Current uint64
	Limit   uint64
}

// RoomUse is one room slice's use of its caps at one read. Caps holds only the
// caps the slice has: a limit of "max" or 0 caps nothing.
type RoomUse struct {
	RoomID string
	// Slice is the slice unit name, such as "dyna-room-room_1.slice".
	Slice string
	Caps  []CapUse
}

// RoomCaps reads every room slice below the user manager's cgroup root, the
// directory that holds dyna.slice. A slice whose name maps to no room id is
// left out: no room can be told about it.
func RoomCaps(userRoot string) []RoomUse {
	dirs, _ := filepath.Glob(filepath.Join(userRoot, "dyna.slice", "dyna-room.slice", "dyna-room-*.slice"))
	out := make([]RoomUse, 0, len(dirs))
	for _, dir := range dirs {
		roomID, ok := agentslice.RoomIDFromSlice(filepath.Base(dir))
		if !ok {
			continue
		}
		use := RoomUse{RoomID: roomID, Slice: filepath.Base(dir)}
		current := roomMemoryCurrent(dir)
		for _, c := range []struct {
			cap  Cap
			file string
		}{{CapMemoryHigh, "memory.high"}, {CapMemoryMax, "memory.max"}} {
			if limit, ok := cgroupUint(dir, c.file); ok && limit > 0 {
				use.Caps = append(use.Caps, CapUse{Cap: c.cap, Current: current, Limit: limit})
			}
		}
		pids, _ := cgroupUint(dir, "pids.current")
		if limit, ok := cgroupUint(dir, "pids.max"); ok && limit > 0 {
			use.Caps = append(use.Caps, CapUse{Cap: CapPidsMax, Current: pids, Limit: limit})
		}
		out = append(out, use)
	}
	return out
}

// roomMemoryCurrent reads the memory that counts against a room's limits
// after reclaim. Clean file pages on the active and inactive LRU lists and
// reclaimable slab can make room for new allocations. Dirty, writeback,
// shared and non-LRU file pages stay charged. A missing, malformed or
// inconsistent memory.stat keeps the raw memory.current reading: without all
// of these fields the doctor cannot safely discount any cache.
func roomMemoryCurrent(dir string) uint64 {
	current, _ := cgroupUint(dir, "memory.current")
	b, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return current
	}
	stats := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return current
		}
		stats[fields[0]] = value
	}
	names := [...]string{"active_file", "inactive_file", "file_dirty", "file_writeback", "shmem", "slab_reclaimable"}
	values := make([]uint64, len(names))
	for i, name := range names {
		value, ok := stats[name]
		if !ok {
			return current
		}
		values[i] = value
	}
	file, carry := bits.Add64(values[0], values[1], 0)
	if carry != 0 {
		return current
	}
	for _, nonReclaimable := range values[2:5] {
		if nonReclaimable > file {
			file = 0
			break
		}
		file -= nonReclaimable
	}
	reclaimable, carry := bits.Add64(file, values[5], 0)
	if carry != 0 || reclaimable > current {
		return current
	}
	return current - reclaimable
}

// cgroupUint reads one cgroup file that holds a whole number. "max", a
// missing file or a value that is not a number reads as no value.
func cgroupUint(dir, name string) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// CapTree is one process tree inside a room's slice: a process whose parent
// runs outside the room, with its command and PID, and the memory and process
// count of everything under it.
type CapTree struct {
	Command string
	PID     int
	// Memory is the resident memory of the whole tree, in bytes.
	Memory uint64
	// Count is the processes in the tree.
	Count int
}

// CapTrees lists a room's biggest process trees twice: by resident memory and
// by process count, biggest first.
type CapTrees struct {
	ByMemory []CapTree
	ByCount  []CapTree
}

// TopTrees reads the processes of one room's slice through procRoot and
// returns its n biggest process trees each way. The room's processes run in
// scopes below its slice, so a tree roots at a process whose parent is
// outside the slice: each names a program the room started.
func TopTrees(sliceDir, procRoot string, n int) CapTrees {
	procs := map[int]proc.Process{}
	for pid := range sliceProcs(sliceDir) {
		if info, err := proc.Read(procRoot, pid); err == nil {
			procs[pid] = info
		}
	}
	children := map[int][]int{}
	roots := make([]int, 0, len(procs))
	for pid, info := range procs {
		if _, inside := procs[info.ParentPID]; inside {
			children[info.ParentPID] = append(children[info.ParentPID], pid)
		} else {
			roots = append(roots, pid)
		}
	}
	sort.Ints(roots)
	trees := make([]CapTree, 0, len(roots))
	for _, root := range roots {
		info := procs[root]
		tree := CapTree{Command: info.Command, PID: root, Memory: info.Memory, Count: 1}
		stack := append([]int(nil), children[root]...)
		for len(stack) > 0 {
			last := len(stack) - 1
			pid := stack[last]
			stack = stack[:last]
			tree.Count++
			tree.Memory += procs[pid].Memory
			stack = append(stack, children[pid]...)
		}
		trees = append(trees, tree)
	}
	var out CapTrees
	out.ByMemory = append([]CapTree(nil), trees...)
	sort.Slice(out.ByMemory, func(i, j int) bool {
		if out.ByMemory[i].Memory != out.ByMemory[j].Memory {
			return out.ByMemory[i].Memory > out.ByMemory[j].Memory
		}
		return out.ByMemory[i].PID < out.ByMemory[j].PID
	})
	out.ByCount = append([]CapTree(nil), trees...)
	sort.Slice(out.ByCount, func(i, j int) bool {
		if out.ByCount[i].Count != out.ByCount[j].Count {
			return out.ByCount[i].Count > out.ByCount[j].Count
		}
		return out.ByCount[i].PID < out.ByCount[j].PID
	})
	if len(out.ByMemory) > n {
		out.ByMemory = out.ByMemory[:n]
	}
	if len(out.ByCount) > n {
		out.ByCount = out.ByCount[:n]
	}
	return out
}

// sliceProcs reads the PIDs of every cgroup.procs at and below dir: the room's
// work runs in scopes below its slice, each with its own file.
func sliceProcs(dir string) map[int]bool {
	out := map[int]bool{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "cgroup.procs" {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil {
			for _, field := range strings.Fields(string(b)) {
				if pid, err := strconv.Atoi(field); err == nil {
					out[pid] = true
				}
			}
		}
		return nil
	})
	return out
}
