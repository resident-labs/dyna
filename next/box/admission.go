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

// DiskWarn is the free space that warns: 85% used on a large disk, and always above the point where the largest box is refused.
func DiskWarn(floor int64) int64 {
	return floor + max(floor/2, diskRequest(contract.BoxSizeXLarge))
}

// Admit uses snapshots of hosts and existing allocations.
func Admit(hosts []Host, boxes []Box, request Box) Placement {
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
	var best int64 = -1
	for _, h := range hosts {
		if h.State != HostReady || (existing != nil && h.Name != existing.Host) {
			continue
		}
		if len(h.Rooms) > 0 && !slices.Contains(h.Rooms, request.Room) {
			if reason == PendingNoReadyHost {
				reason = PendingRoomHost
			}
			continue
		}
		if h.MemUsed >= h.MemBytes-h.MemBytes/5 {
			reason = PendingMemory
			continue
		}
		cpu, mem, disk := 0, int64(0), h.DiskBytes
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
			}
		}
		if cpu+request.Size.CPUs > 4*h.CPUs {
			reason = PendingCPU
			continue
		}
		if mem+request.Size.MemBytes > 4*h.MemBytes {
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
		free := 4*h.MemBytes - mem
		if chosen == "" || free > best || (free == best && h.Name < chosen) {
			chosen, best = h.Name, free
		}
	}
	if chosen == "" {
		return Placement{Reason: reason}
	}
	return Placement{Host: chosen}
}
