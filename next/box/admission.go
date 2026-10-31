package box

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type PendingReason string

const (
	PendingCreateRetry    PendingReason = "create-retry"
	PendingNoReadyHost    PendingReason = "no-ready-host"
	PendingCPU            PendingReason = "cpu"
	PendingMemory         PendingReason = "memory"
	PendingDisk           PendingReason = "disk"
	PendingSliceCollision PendingReason = "slice-collision"
	PendingParentHost     PendingReason = "parent-host"
	PendingInvalidSize    PendingReason = "invalid-size"
)

type Placement struct {
	Host   contract.HostName
	Reason PendingReason
}

func SliceName(ids []contract.BoxID) (string, error) {
	parts := []string{"dyna"}
	for _, id := range ids {
		if id == "" {
			return "", fmt.Errorf("empty box ID")
		}
		sum := sha256.Sum256([]byte(id))
		parts = append(parts, fmt.Sprintf("%x", sum[:6]))
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("empty box path")
	}
	name := strings.Join(parts, "-") + ".slice"
	if len(name) > 255 {
		return "", fmt.Errorf("slice name exceeds 255 bytes")
	}
	return name, nil
}
func CgroupArgs(slice string) []string { return []string{"--cgroup-parent=" + slice} }

// Admit uses snapshots of hosts and existing allocations. A child is restricted to its parent's host.
func Admit(hosts []Host, boxes []Box, request Box, diskFloor int64) Placement {
	if diskRequest(request.Size.Name) == 0 {
		return Placement{Reason: PendingInvalidSize}
	}
	var parentHost contract.HostName
	if request.BudgetParent != "" {
		for _, b := range boxes {
			if b.ID == request.BudgetParent && (b.State == contract.BoxStarting || b.State == contract.BoxReady) {
				parentHost = b.Host
				break
			}
		}
		if parentHost == "" {
			return Placement{Reason: PendingParentHost}
		}
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
	component := fmt.Sprintf("%x", sha256.Sum256([]byte(request.ID)))[:12]
	for _, h := range hosts {
		if h.State != HostReady || (parentHost != "" && h.Name != parentHost) || (existing != nil && h.Name != existing.Host) {
			continue
		}
		cpu, mem, disk := 0, int64(0), h.DiskBytes
		collision := false
		for _, b := range boxes {
			if b.Host != h.Name || b.ID == request.ID || b.State == contract.BoxDeleted || b.State == contract.BoxFailed {
				continue
			}
			if b.PlacedAt.After(h.CheckedAt) {
				disk -= diskRequest(b.Size.Name)
			}
			if b.BudgetParent == "" && b.State != contract.BoxStopped && b.State != contract.BoxPending {
				cpu += b.Size.CPUs
				mem += b.Size.MemBytes
			}
			sum := sha256.Sum256([]byte(b.ID))
			if fmt.Sprintf("%x", sum[:6]) == component {
				collision = true
			}
		}
		if collision {
			reason = PendingSliceCollision
			continue
		}
		if request.BudgetParent == "" && cpu+request.Size.CPUs > h.CPUs {
			reason = PendingCPU
			continue
		}
		if request.BudgetParent == "" && mem+request.Size.MemBytes > h.MemBytes {
			reason = PendingMemory
			continue
		}
		if existing == nil {
			disk -= diskRequest(request.Size.Name)
		} else if existing.PlacedAt.After(h.CheckedAt) {
			disk -= diskRequest(existing.Size.Name)
		}
		if disk < diskFloor {
			reason = PendingDisk
			continue
		}
		free := h.MemBytes - mem
		if chosen == "" || free > best || (free == best && h.Name < chosen) {
			chosen, best = h.Name, free
		}
	}
	if chosen == "" {
		return Placement{Reason: reason}
	}
	return Placement{Host: chosen}
}
