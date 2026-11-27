package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func admissionBytes(n int64) string { return humanize.IBytes(uint64(max(0, n))) }

// AdmissionNotices explains a long pending spell to its parent and room root.
// Lifecycle events give the spell a durable clock and idempotency key.
func (s *Service) AdmissionNotices(ctx context.Context) error {
	rows, err := s.store.LifecycleBoxes(ctx)
	if err != nil {
		return err
	}
	var boxes []box.Box
	for _, row := range rows {
		boxes = append(boxes, row.Box)
	}
	hosts, err := s.reconciler.AdmissionHosts(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		b := row.Box
		if b.State != contract.BoxPending || b.Reason == "" || row.Paused || row.Deleting {
			continue
		}
		since, key, err := s.store.PendingSpell(ctx, b)
		if err != nil {
			return err
		}
		if s.now().Sub(since) < 5*time.Minute {
			continue
		}
		session, err := s.store.Session(ctx, b.Session)
		if err != nil {
			return err
		}
		readers, err := s.store.AdmissionReaders(ctx, b.Room, b.Session)
		if err != nil {
			return err
		}
		for _, reader := range readers {
			lines := []string{fmt.Sprintf("Session %s (%s), box %s, has waited at least 5 minutes for admission: %s.", session.Name, b.Session, b.ID, admissionReason(b.Reason))}
			candidates := box.CandidateHosts(hosts, b, row.Only)
			if b.Host != "" {
				lines = append(lines, fmt.Sprintf("Pinned to %s (or its existing files are there).", b.Host))
			} else {
				lines = append(lines, "Not pinned; eligible hosts follow (room and host rules still apply).")
			}
			if b.Reason == string(box.PendingRoomLimit) {
				candidates = []box.Host{{Name: "room limits"}}
			}
			if len(candidates) == 0 {
				lines = append(lines, "No eligible registered host; check host list and the room's host rule or pool.")
			}
			for _, h := range candidates {
				if b.Reason == string(box.PendingRoomLimit) {
					var services []box.ServiceHold
					for _, host := range hosts {
						services = append(services, host.Services...)
					}
					cpu, mem := box.RoomQuarters(b.Room, boxes, services, b.ID, "")
					lines = append(lines, fmt.Sprintf("Room %s: CPU %.2f of %d cores reserved, memory %s of %s reserved (boxes count at one quarter, services whole); this box adds %.2f cores and %s. A zero cap means unlimited. Moving hosts does not release a room limit.", b.Room, float64(cpu)/4000, row.Limits.CPUs, admissionBytes(mem/4), admissionBytes(row.Limits.MemBytes), float64(b.Size.CPUs)/4, admissionBytes(b.Size.MemBytes/4)))
				} else {
					lines = append(lines, admissionHost(h, boxes, b))
				}
				holders := slices.DeleteFunc(slices.Clone(boxes), func(v box.Box) bool {
					return v.ID == b.ID || (b.Reason == string(box.PendingRoomLimit) && v.Room != b.Room) || (b.Reason != string(box.PendingRoomLimit) && v.Host != h.Name) || ((b.Reason == "cpu" || b.Reason == "memory") && b.Image != "" && b.Image != box.RunnerImage && v.Image == box.RunnerImage) || v.State == contract.BoxFailed || v.State == contract.BoxDeleted || (b.Reason == "disk" && !box.UnmeasuredDisk(h, v)) || (b.Reason == "gpu" && (!v.HoldsGPU() || v.GPU == 0)) || (b.Reason != "disk" && b.Reason != "gpu" && !box.HoldsCompute(v))
				})
				weight := func(v box.Box) int64 {
					switch b.Reason {
					case "cpu":
						return int64(v.Size.CPUs)
					case "disk":
						return box.DiskRequest(v.Size.Name)
					case "gpu":
						return int64(v.GPU)
					default:
						return v.Size.MemBytes
					}
				}
				slices.SortStableFunc(holders, func(a, c box.Box) int {
					if weight(a) > weight(c) {
						return -1
					}
					if weight(a) < weight(c) {
						return 1
					}
					return strings.Compare(string(a.ID), string(c.ID))
				})
				for _, held := range holders[:min(8, len(holders))] {
					label, err := s.store.RoomNameOf(ctx, held.Room)
					if err != nil {
						return err
					}
					if problem := s.roomAccess(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: reader}, held.Room); problem == nil {
						holder, err := s.store.Session(ctx, held.Session)
						if err != nil {
							return err
						}
						label += "/" + holder.Name
					} else if problem.Code != contract.ErrForbidden {
						return fmt.Errorf("admission holder access: %s", problem.Message)
					}
					size := admissionBytes(weight(held))
					if b.Reason == string(box.PendingRoomLimit) {
						size = fmt.Sprintf("%.2f cores and %s reserved", float64(held.Size.CPUs)/4, admissionBytes(held.Size.MemBytes/4))
					}
					if b.Reason == "cpu" {
						size = fmt.Sprintf("%d CPUs", weight(held))
					}
					if b.Reason == "gpu" {
						size = fmt.Sprintf("%d GiB GPU", weight(held))
					}
					lines = append(lines, fmt.Sprintf("- %s: %s", label, size))
				}
				if len(h.Services) > 0 {
					lines = append(lines, fmt.Sprintf("%d service reservation(s) are also included; host list shows services you can read.", len(h.Services)))
				}
			}
			lines = append(lines, "Stop or close a reservation holder you manage (closing also frees its disk). A pending box with nothing created can use session restart with a new host; room rules still apply. A box with files on its host must wait there. Use host list for current reservations and free disk.")
			event, err := s.engineEvent(b.Room, contract.SessionNotice{Session: reader, Text: strings.Join(lines, "\n"), ClientID: key + ":" + string(reader)})
			if err != nil {
				return err
			}
			if err := s.store.SendAdmissionNotice(ctx, b, event, reader); err != nil {
				return err
			}
		}
	}
	return nil
}

func admissionReason(reason string) string {
	switch box.PendingReason(reason) {
	case box.PendingCPU:
		return "CPU reservations are full"
	case box.PendingMemory:
		return "memory reservations or measured memory are full"
	case box.PendingDisk:
		return "available disk would fall below its reserved floor"
	case box.PendingGPU:
		return "GPU memory is reserved"
	case box.PendingNoGPU:
		return "no ready host has a GPU"
	case box.PendingNoAgy:
		return "no ready host has agy installed and signed in"
	case box.PendingPoolNode:
		return "waiting for a pool node"
	case box.PendingPoolFailed:
		return "pool node creation failed"
	case box.PendingNoReadyHost:
		return "no host is ready"
	case box.PendingRoomHost:
		return "no ready host satisfies the room's host rule"
	case box.PendingRoomLimit:
		return "the room's resource limit is reached"
	case box.PendingCreateRetry, box.PendingOtherHostRetry:
		return "retrying box creation"
	default:
		return strings.ReplaceAll(reason, "-", " ")
	}
}

func admissionHost(h box.Host, boxes []box.Box, b box.Box) string {
	l := box.Reservations(h, boxes, b)
	switch box.PendingReason(b.Reason) {
	case box.PendingCPU:
		return fmt.Sprintf("%s: CPU %.1f of %.1f cores reserved; this box needs %d. CI runners: %d of %.1f cores (2× CI scale).", h.Name, float64(l.HeldCPU), float64(l.CPUCap)/1000, b.Size.CPUs, l.RunnerCPU, float64(l.RunnerCPUCap)/1000)
	case box.PendingMemory:
		return fmt.Sprintf("%s: memory %s of %s reserved (4× %s after services); this box needs %s. Measured use %s of %s (80%% backstop). CI runners: %s of %s reserved.", h.Name, admissionBytes(l.HeldMemory), admissionBytes(l.MemoryCap), admissionBytes(l.MemoryBudget), admissionBytes(b.Size.MemBytes), admissionBytes(h.MemUsed), admissionBytes(h.MemBytes), admissionBytes(l.RunnerMemory), admissionBytes(l.RunnerMemoryCap))
	case box.PendingDisk:
		return fmt.Sprintf("%s: disk %s available after reservations, %s floor; this box adds %s before the next disk sample. Measured free %s of %s.", h.Name, admissionBytes(l.DiskFree), admissionBytes(h.DiskFloor), admissionBytes(l.DiskNeeded), admissionBytes(h.DiskBytes), admissionBytes(h.DiskTotal))
	case box.PendingGPU:
		text := fmt.Sprintf("%s: this box needs %d GiB GPU memory.", h.Name, b.GPU)
		for _, g := range h.GPUs {
			var held int64
			for _, v := range boxes {
				if v.Host == h.Name && v.ID != b.ID && v.GPUCard == g.Index && v.HoldsGPU() {
					held += int64(v.GPU) * box.GiB
				}
			}
			text += fmt.Sprintf(" GPU %d: %s of %s reserved.", g.Index, admissionBytes(held), admissionBytes(box.GPUUsable(g)))
		}
		return text
	default:
		return fmt.Sprintf("%s: state %s, problem %s.", h.Name, h.State, h.Problem)
	}
}
