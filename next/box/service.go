package box

import (
	"context"
	"slices"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ServiceHold is what a placed service reserves on its host (spec 5.18): CPU and memory at 1x, and its disk with the measured
// size of its image. It counts from placement until the service is removed, stopped or not.
type ServiceHold struct {
	ID        contract.ServiceID
	Room      contract.RoomID
	CPUMilli  int64
	MemBytes  int64
	DiskBytes int64
}

// ServiceRequest is a service that asks for a host. ImageEstimate is the disk its image will take, from the newest measure of
// it on any host, or contract.ServiceImageEstimate.
type ServiceRequest struct {
	ID            contract.ServiceID
	Room          contract.RoomID
	Parts         contract.ServiceParts
	ImageEstimate int64
}

// hostLoad sums the boxes on a host, except self: the CPU and memory of those running, the runners among them, and the host's
// free disk after the requests of those whose disk the last measure may not hold yet.
func hostLoad(h Host, boxes []Box, self contract.BoxID) (cpu, runnerCPU int, mem, runnerMem, disk int64) {
	disk = h.DiskBytes
	for _, b := range boxes {
		if b.Host != h.Name || b.ID == self || b.State == contract.BoxDeleted || b.State == contract.BoxFailed {
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
	return cpu, runnerCPU, mem, runnerMem, disk
}

// serviceLoad sums the services on a host, except one: their CPU in thousandths of a core, memory and disk.
func (h Host) serviceLoad(except contract.ServiceID) (cpuMilli, mem, disk int64) {
	for _, s := range h.Services {
		if s.ID != except {
			cpuMilli += s.CPUMilli
			mem += s.MemBytes
			disk += s.DiskBytes
		}
	}
	return cpuMilli, mem, disk
}

// takesService says whether a host may hold a service: ready, not exec only, not the engine's host (a host whose flag is not
// known yet, as after an engine start, is refused),  not a pool's node (it
// is disposable and a service stays for good), and in the room's rule and the host's own room list. It needs no agents or ci switch.
func takesService(h Host, room contract.RoomID, only []string) bool {
	if h.State != HostReady || h.ExecOnly || h.Pool != "" || h.EngineHost == nil || *h.EngineHost {
		return false
	}
	if len(h.Rooms) > 0 && !slices.Contains(h.Rooms, room) {
		return false
	}
	return len(only) == 0 || slices.Contains(only, string(h.Name))
}

// AdmitService places a service (spec 5.18). A service fits a host when its CPU and memory with those of the services already
// there stay within a quarter of the host, when the box and runner load already counted still fits what is left after it, when
// the disk rule holds with its disk and its image, and when the measured memory is below 80%. Of the hosts that fit it takes
// the one with the most headroom. A declared host is a further rule, inside the room's: the service goes there or nowhere. A service that is already placed (Host set on a hold) is judged on that host alone.
func AdmitService(hosts []Host, boxes []Box, request ServiceRequest, only ...string) Placement {
	reason := PendingNoReadyHost
	var chosen contract.HostName
	var best float64
	for _, h := range hosts {
		if !takesService(h, request.Room, only) || request.Parts.Spec.Host != "" && request.Parts.Spec.Host != h.Name {
			if h.State == HostReady && reason == PendingNoReadyHost {
				reason = PendingRoomHost
			}
			continue
		}
		if h.MemUsed >= h.MemBytes-h.MemBytes/5 {
			reason = PendingMemory
			continue
		}
		cpu, runnerCPU, mem, runnerMem, _ := hostLoad(h, boxes, "")
		serviceCPU, serviceMem, _ := h.serviceLoad(request.ID)
		cpuTotal, memTotal := int64(h.CPUs)*1000, h.MemBytes
		cpuLeft, memLeft := cpuTotal-serviceCPU-request.Parts.CPUMilli, memTotal-serviceMem-request.Parts.MemBytes
		if serviceCPU+request.Parts.CPUMilli > cpuTotal/4 || int64(cpu)*1000 > 4*cpuLeft || int64(runnerCPU)*1000 > 2*cpuLeft {
			reason = PendingCPU
			continue
		}
		if serviceMem+request.Parts.MemBytes > memTotal/4 || mem > 4*memLeft || runnerMem > memLeft {
			reason = PendingMemory
			continue
		}
		if !ServiceFitsDisk(h, boxes, request) {
			reason = PendingDisk
			continue
		}
		headroom := min(float64(4*cpuLeft-int64(cpu)*1000)/float64(4*cpuTotal), float64(4*memLeft-mem)/float64(4*memTotal))
		if chosen == "" || headroom > best || (headroom == best && h.Name < chosen) {
			chosen, best = h.Name, headroom
		}
	}
	if chosen == "" {
		return Placement{Reason: reason}
	}
	return Placement{Host: chosen}
}

// ServiceFitsDisk checks the latest host disk figure against other reservations and this service's data and image. It is also
// checked before a placed service pulls or builds, since disk may have filled since placement. Its own hold is replaced, not added.
func ServiceFitsDisk(host Host, boxes []Box, request ServiceRequest) bool {
	_, _, _, _, disk := hostLoad(host, boxes, "")
	_, _, held := host.serviceLoad(request.ID)
	return disk-held-request.Parts.DiskBytes-request.ImageEstimate >= host.DiskFloor
}

// RoomQuarters is what a room's placed boxes and services count against its limits, in thousandths of a CPU and in bytes, as
// quarters: a box counts a quarter of its size and a service counts whole, which is four of its quarters. A box not pending,
// stopped, failed or deleted counts, except skipBox, and every service counts, except skipService.
func RoomQuarters(room contract.RoomID, boxes []Box, holds []ServiceHold, skipBox contract.BoxID, skipService contract.ServiceID) (cpuMilli, mem int64) {
	for _, b := range boxes {
		if b.ID == skipBox || b.Room != room || b.State == contract.BoxPending || b.State == contract.BoxStopped || b.State == contract.BoxFailed || b.State == contract.BoxDeleted {
			continue
		}
		cpuMilli += int64(b.Size.CPUs) * 1000
		mem += b.Size.MemBytes
	}
	for _, s := range holds {
		if s.Room == room && s.ID != skipService {
			cpuMilli += 4 * s.CPUMilli
			mem += 4 * s.MemBytes
		}
	}
	return cpuMilli, mem
}

// ServiceFitsRoom judges a service against the room's CPU and memory budgets. Services count at 1x and active boxes at a quarter;
// the request replaces its own hold, rather than counting twice on an update. Hosts do not restrict a room's accounting.
func ServiceFitsRoom(request ServiceRequest, limits contract.RoomLimits, boxes []Box, holds []ServiceHold) bool {
	cpu, memory := RoomQuarters(request.Room, boxes, holds, "", request.ID)
	cpu += 4 * request.Parts.CPUMilli
	memory += 4 * request.Parts.MemBytes
	return (limits.CPUs == 0 || cpu <= 4000*int64(limits.CPUs)) && (limits.MemBytes == 0 || memory <= 4*limits.MemBytes)
}

// ServiceUnitState is what a host's systemd says of a service's unit, read once at the host boundary.
type ServiceUnitState string

const (
	UnitRunning  ServiceUnitState = "running"  // active
	UnitCrashing ServiceUnitState = "crashing" // restarting, or failed
	UnitStopped  ServiceUnitState = "stopped"  // inactive
	UnitAbsent   ServiceUnitState = "absent"   // no such unit
)

// ServiceUnit is one service's unit as a heartbeat or a show reports it. CrashLoop says systemd gave up at its start limit.
type ServiceUnit struct {
	ID        contract.ServiceID
	State     ServiceUnitState
	CrashLoop bool
	Restarts  int
}

// ServiceFile is a quadlet file found on a host with the hash of its content.
type ServiceFile struct {
	ID  contract.ServiceID
	SHA string
}

// ServiceUnitSpec is everything a quadlet file holds. It holds no secret value, only the names of the secrets.
type ServiceUnitSpec struct {
	ID    contract.ServiceID
	Room  contract.RoomID
	Parts contract.ServiceParts
	// Image is the reference that runs: a local tag with the content id of a room image, or a registry digest.
	Image string
}

// ServiceHostCheck is what the service host check found.
type ServiceHostCheck struct {
	Issues []contract.ServiceHostIssue
	// LowestPort is the host's net.ipv4.ip_unprivileged_port_start.
	LowestPort int
}

// ServiceName is the name of a service's container and, with ".service", of the unit the quadlet generator makes of it.
func ServiceName(id contract.ServiceID) string { return "dyna-svc-" + string(id) }

// ServiceSecretName is the podman secret that holds one of a service's secrets.
func ServiceSecretName(id contract.ServiceID, name string) string {
	return ServiceName(id) + "-" + name
}

// ServiceDriver is a host's service operations (spec 5.18). Each call is one step with its own exit status, so a failure's
// cause is the call that failed.
type ServiceDriver interface {
	// CheckServiceHost runs the service host check.
	CheckServiceHost(context.Context) (ServiceHostCheck, error)
	// EnsureServiceSlice writes dyna-svc.slice with its CPU weight and reloads the user manager.
	EnsureServiceSlice(context.Context) error
	// ListServiceFiles lists the quadlet files on the host.
	ListServiceFiles(context.Context) ([]ServiceFile, error)
	// ListeningPorts lists the host ports with a listening socket, whoever holds them.
	ListeningPorts(context.Context) ([]contract.PortBinding, error)
	// PullService pulls a registry digest.
	PullService(context.Context, string) error
	// ImageSize measures an image on the host, in bytes.
	ImageSize(context.Context, string) (int64, error)
	// WriteServiceSecret creates or replaces a podman secret, with the value on stdin.
	WriteServiceSecret(ctx context.Context, id contract.ServiceID, name string, value []byte) error
	// WriteServiceUnit writes the quadlet file, reloads the user manager and returns the file's hash.
	WriteServiceUnit(context.Context, ServiceUnitSpec) (string, error)
	RestartService(context.Context, contract.ServiceID) error
	StopService(context.Context, contract.ServiceID) error
	ShowService(context.Context, contract.ServiceID) (ServiceUnit, error)
	// ServiceHealthy runs the health command in the container and reports its exit status as nil or an error.
	ServiceHealthy(ctx context.Context, id contract.ServiceID, command []string) error
	// ServiceLogs reads the unit's journal.
	ServiceLogs(ctx context.Context, id contract.ServiceID, lines int, since time.Duration) (string, error)
	// RemoveService stops the unit and deletes its quadlet, secrets, data volume and container. It tolerates what is gone.
	RemoveService(context.Context, contract.ServiceID) error
}
