package box

import (
	"fmt"
	"slices"

	"github.com/resident-labs/dyna/contract"
)

type PendingReason string

const (
	PendingCreateRetry PendingReason = "create-retry"
	PendingNoReadyHost PendingReason = "no-ready-host"
	PendingCPU         PendingReason = "cpu"
	PendingMemory      PendingReason = "memory"
	PendingDisk        PendingReason = "disk"
	PendingRoomHost    PendingReason = "room-host"
	PendingRoomLimit   PendingReason = "room-limit"
	PendingInvalidSize PendingReason = "invalid-size"
	// A room's pool brings its next node, so its boxes wait for that rather than for a host.
	PendingPoolNode   PendingReason = "pool-node"
	PendingPoolFailed PendingReason = "pool-failed"
)

type Placement struct {
	Host   contract.HostName
	Reason PendingReason
}

func SliceName(id contract.BoxID) (string, error) {
	if id == "" {
		return "", fmt.Errorf("empty box ID")
	}
	return "dyna-" + string(id) + ".slice", nil
}
func CgroupArgs(slice string) []string { return []string{"--cgroup-parent=" + slice} }

// DiskFloor is the free space a host keeps: 20 GiB, or a tenth of its disk if that is more.
func DiskFloor(total int64) int64 { return max(20*GiB, total/10) }

// DiskWarn is the free space that warns: 85% used on a large disk, and above the point where the largest box is refused. On a
// small disk that box takes at most a quarter of it, so a new node of 150 GB does not warn while it is still empty. total is
// the disk's size, or 0 when unknown.
func DiskWarn(floor, total int64) int64 {
	headroom := diskRequest(contract.BoxSizeXLarge)
	if total > 0 {
		headroom = min(headroom, total/4)
	}
	return floor + max(floor/2, headroom)
}

// Serves says whether a host takes a room's session boxes: agents are on, and its room list, which empty means every room,
// holds the room.
func (h Host) Serves(room contract.RoomID) bool {
	// A shared pool node lists the rooms that named its pool when it came up, but a room with no rule may use it too, so
	// takes holds each room to its rule and the list does not.
	shared := h.Pool != "" && h.Box == ""
	return !h.NoAgents && (shared || len(h.Rooms) == 0 || slices.Contains(h.Rooms, room))
}

// RunnerImage marks a CI runner where admission sees it as a box: one on a host, or one that waits for a host.
const RunnerImage = "runner"

// RunnerHosts are the hosts that take CI runners: ready, with ci on. The agents switch and the room list are for session
// boxes, so a runner ignores them, and Admit still holds it to its room's rule, as it does the room's boxes.
func RunnerHosts(hosts []Host) []Host {
	var open []Host
	for _, h := range hosts {
		if h.State == HostReady && h.CI {
			h.Rooms, h.NoAgents = nil, false
			open = append(open, h)
		}
	}
	return open
}

// takes says whether a host may take a new box. A per-session node serves its one box, a shared node the rooms that name its
// pool or has no rule, and any other host every room whose rule, if it has one, names it.
func takes(h Host, id contract.BoxID, only []string) bool {
	switch {
	case h.Box != "":
		return h.Box == id
	case h.Pool != "":
		return len(only) == 0 || slices.Contains(only, h.Pool)
	}
	return len(only) == 0 || slices.Contains(only, string(h.Name))
}

// Admit uses snapshots of hosts and existing allocations. only is the room's rule: the hosts and the pool its new boxes may use,
// and none means any host.
func Admit(hosts []Host, boxes []Box, request Box, only ...string) Placement {
	if diskRequest(request.Size.Name) == 0 {
		return Placement{Reason: PendingInvalidSize}
	}
	var existing *Box
	for i := range boxes {
		if boxes[i].ID == request.ID && boxes[i].Host != "" {
			existing = &boxes[i]
			break
		}
	}
	reason := PendingNoReadyHost
	var chosen contract.HostName
	var best float64
	for _, h := range hosts {
		// A create quarantine blocks new boxes, including retries, not wakes or CI runners.
		if h.State != HostReady || (existing != nil && h.Name != existing.Host) || (h.Problem != "" && request.Image != RunnerImage && (existing == nil || existing.State == contract.BoxPending && existing.Image == "")) {
			continue
		}
		if !h.Serves(request.Room) || existing == nil && !takes(h, request.ID, only) {
			if reason == PendingNoReadyHost {
				reason = PendingRoomHost
			}
			continue
		}
		if h.MemUsed >= h.MemBytes-h.MemBytes/5 {
			reason = PendingMemory
			continue
		}
		cpu, runnerCPU, mem, runnerMem, disk := 0, 0, int64(0), int64(0), h.DiskBytes
		for _, b := range boxes {
			if b.Host != h.Name || b.ID == request.ID || b.State == contract.BoxDeleted || b.State == contract.BoxFailed {
				continue
			}
			if (b.State != contract.BoxPending && b.Image == "") || (!b.PlacedAt.Before(h.CheckedAt) && !(b.State == contract.BoxPending && b.Image == "")) {
				disk -= diskRequest(b.Size.Name)
			}
			if b.State != contract.BoxStopped && b.State != contract.BoxPending {
				cpu += b.Size.CPUs
				mem += b.Size.MemBytes
				if b.Image == RunnerImage {
					runnerCPU += b.Size.CPUs
					runnerMem += b.Size.MemBytes
				}
			}
		}
		// A session box idles most of the time, so a host takes four times its CPUs of them. A CI runner is busy for its whole
		// job but uses about half its CPUs on average, between links, tests and waits, so a host takes twice its CPUs of
		// runners. At four times, hosts ran at two to six times their CPUs and jobs took two to three times as long; at once,
		// they sat half idle. Runners fit in real memory at 1x because a busy compile peaks near its limit.
		if cpu+request.Size.CPUs > 4*h.CPUs || request.Image == RunnerImage && runnerCPU+request.Size.CPUs > 2*h.CPUs {
			reason = PendingCPU
			continue
		}
		if mem+request.Size.MemBytes > 4*h.MemBytes || request.Image == RunnerImage && runnerMem+request.Size.MemBytes > h.MemBytes {
			reason = PendingMemory
			continue
		}
		if existing == nil || (existing.State == contract.BoxPending && existing.Image == "") {
			disk -= diskRequest(request.Size.Name)
		} else if !existing.PlacedAt.Before(h.CheckedAt) {
			disk -= diskRequest(existing.Size.Name)
		}
		if disk < h.DiskFloor {
			reason = PendingDisk
			continue
		}
		// Earlier capacity guards reject hosts with zero CPUs or memory.
		cpuFree := float64(4*h.CPUs-cpu-request.Size.CPUs) / float64(4*h.CPUs)
		memFree := float64(4*h.MemBytes-mem-request.Size.MemBytes) / float64(4*h.MemBytes)
		headroom := min(cpuFree, memFree)
		if chosen == "" || headroom > best || (headroom == best && h.Name < chosen) {
			chosen, best = h.Name, headroom
		}
	}
	if chosen == "" {
		return Placement{Reason: reason}
	}
	return Placement{Host: chosen}
}
