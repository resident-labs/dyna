package box

import "github.com/resident-labs/dyna/contract"

// ReservationLoad is admission's accounting, excluding the request itself. CPU caps
// are in thousandths of a core; memory and disk are bytes. DiskFree is measured
// available space less allocations not yet reflected in the heartbeat and services.
type ReservationLoad struct {
	CPU, RunnerCPU, HeldCPU                                                   int
	Memory, RunnerMemory, HeldMemory                                          int64
	CPUBudget, MemoryBudget, CPUCap, MemoryCap, RunnerCPUCap, RunnerMemoryCap int64
	DiskFree, DiskNeeded                                                      int64
}

// Reservations is shared by placement and its explanations. Existing containers
// ignore runner holds when waking, but still obey the measured memory backstop.
func Reservations(h Host, boxes []Box, request Box) ReservationLoad {
	cpu, runners, mem, runnerMem, disk := hostLoad(h, boxes, request.ID)
	serviceCPU, serviceMem, serviceDisk := h.serviceLoad("")
	l := ReservationLoad{CPU: cpu, RunnerCPU: runners, HeldCPU: cpu, Memory: mem, RunnerMemory: runnerMem, HeldMemory: mem, CPUBudget: int64(h.CPUs)*1000 - serviceCPU, MemoryBudget: h.MemBytes - serviceMem, DiskFree: disk - serviceDisk}
	scale := int64(4)
	if request.Image == RunnerImage {
		scale = max(scale, 2*int64(h.CIScale))
	}
	l.CPUCap, l.MemoryCap = scale*l.CPUBudget, 4*l.MemoryBudget
	l.RunnerCPUCap, l.RunnerMemoryCap = 2*int64(h.CIScale)*l.CPUBudget, int64(h.CIScale)*l.MemoryBudget
	l.DiskNeeded = diskRequest(request.Size.Name)
	for _, b := range boxes {
		if b.ID != request.ID || b.Host == "" {
			continue
		}
		if b.Image != "" && request.Image != RunnerImage {
			l.HeldCPU -= runners
			l.HeldMemory -= runnerMem
		}
		if !(b.State == contract.BoxPending && b.Image == "") {
			l.DiskNeeded = 0
			if !b.PlacedAt.Before(h.CheckedAt) {
				l.DiskNeeded = diskRequest(b.Size.Name)
			}
		}
		break
	}
	return l
}

// DiskRequest is the estimated new disk allocation for a box size.
func DiskRequest(name contract.BoxSizeName) int64 { return diskRequest(name) }

// CandidateHosts names the hosts a request may use before capacity is checked.
func CandidateHosts(hosts []Host, request Box, only []string) []Host {
	var out []Host
	for _, h := range hosts {
		if request.Host != "" {
			if h.Name == request.Host {
				out = append(out, h)
			}
			continue
		}
		if h.Serves(request.Room) && takes(h, request.ID, only) {
			out = append(out, h)
		}
	}
	return out
}

// HoldsCompute is the box reservation rule used by admission.
func HoldsCompute(b Box) bool {
	return b.State != contract.BoxStopped && b.State != contract.BoxPending && b.State != contract.BoxFailed && b.State != contract.BoxDeleted
}

// UnmeasuredDisk says a box has a disk allocation not yet in the host's sample.
func UnmeasuredDisk(h Host, b Box) bool {
	return b.State != contract.BoxFailed && b.State != contract.BoxDeleted && ((b.State != contract.BoxPending && b.Image == "") || (!b.PlacedAt.Before(h.CheckedAt) && !(b.State == contract.BoxPending && b.Image == "")))
}
