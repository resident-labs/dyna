package box

import (
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ImageCreateError is a create failure that names a missing or invalid image, which says nothing about the host.
type ImageCreateError struct{ Err error }

func (e ImageCreateError) Error() string { return e.Err.Error() }
func (e ImageCreateError) Unwrap() error { return e.Err }

// RuntimeCreateError identifies failure in host container infrastructure, not image or repository work.
type RuntimeCreateError struct{ Err error }

func (e RuntimeCreateError) Error() string { return e.Err.Error() }
func (e RuntimeCreateError) Unwrap() error { return e.Err }

type HostState string

const (
	HostChecking HostState = "checking"
	// HostProvisioning is a pool's node before its script has answered. Only the pool step sees one.
	HostProvisioning HostState = "provisioning"
	HostReady        HostState = "ready"
	// HostDraining takes no new box, on its way out.
	HostDraining    HostState = "draining"
	HostUnreachable HostState = "unreachable"
	HostRemoved     HostState = "removed"
)

type Host struct {
	Health              contract.HostHealth // process-local scheduling health; independent of reachability
	EngineHost          *bool
	Name                contract.HostName
	SSH                 string
	State               HostState
	Problem             contract.HostProblem
	CPUs                int
	MemBytes, DiskBytes int64
	DiskFloor           int64
	DiskTotal           int64 // the disk's size from the latest heartbeat, 0 until one arrives
	MemUsed             int64 // latest heartbeat; not stored
	MaintainedAt        time.Time
	Labels              []string
	Rooms               []contract.RoomID
	// Two switches, each zero at its default: NoAgents keeps new session boxes off the host, and CI lets the CI pool start
	// runners there. Rooms decides which rooms the host serves while agents are on.
	NoAgents  bool
	CI        bool
	ExecOnly  bool // added with agents and ci off: host-exec runs only, checked by SSH and bash, never a box or runner
	CheckedAt time.Time
	Pool      string         // the pool that holds this node
	GPUs      []contract.GPU // the cards its CDI spec offers, from the latest probe
	Box       contract.BoxID // the one box a per-session node serves
}
type Box struct {
	ID      contract.BoxID
	Session contract.SessionID
	Room    contract.RoomID
	Host    contract.HostName
	Image   string
	Size    contract.BoxSize
	State   contract.BoxState
	Reason  string
	Slice   string
	Updated time.Time
	// PlacedAt records when this allocation began, for disk accounting.
	PlacedAt time.Time
	// GPU is the VRAM the box asked of a card (5.5), 0 for none. GPUCard is the card it was assigned on Host, meaningful while GPU
	// is above 0 and Host is set, and kept while the box lives there.
	GPU     contract.GPUGiB
	GPUCard int
}

type TransitionError struct{ From, To contract.BoxState }

func (e TransitionError) Error() string {
	return fmt.Sprintf("invalid box transition %s -> %s", e.From, e.To)
}

var transitions = map[contract.BoxState][]contract.BoxState{
	contract.BoxPending:  {contract.BoxStopped, contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxClosing},
	contract.BoxCreating: {contract.BoxStopped, contract.BoxPending, contract.BoxStarting, contract.BoxReady, contract.BoxFailed, contract.BoxClosing},
	contract.BoxStarting: {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
	contract.BoxReady:    {contract.BoxStopped, contract.BoxFailed, contract.BoxClosing, contract.BoxLost},
	contract.BoxStopped:  {contract.BoxStarting, contract.BoxPending, contract.BoxClosing, contract.BoxLost},
	contract.BoxClosing:  {contract.BoxDeleted, contract.BoxStopped, contract.BoxLost, contract.BoxFailed},
	// Only session.restart moves a failed box back to pending.
	contract.BoxFailed: {contract.BoxClosing, contract.BoxPending},
	contract.BoxLost:   {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
}

func Transition(b Box, to contract.BoxState, reason string, now time.Time) (Box, contract.BoxChanged, error) {
	for _, allowed := range transitions[b.State] {
		if allowed == to {
			event := contract.BoxChanged{Box: b.ID, Session: b.Session, Host: b.Host, From: b.State, To: to, Reason: reason}
			b.State, b.Reason, b.Updated = to, reason, now
			return b, event, nil
		}
	}
	return b, contract.BoxChanged{}, TransitionError{b.State, to}
}

const GiB int64 = 1 << 30

func Size(name contract.BoxSizeName) (contract.BoxSize, bool) {
	switch name {
	case contract.BoxSizeAgent:
		return contract.BoxSize{Name: name, CPUs: 1, MemBytes: 2 * GiB, Pids: 1024}, true
	case contract.BoxSizeSmall:
		return contract.BoxSize{Name: name, CPUs: 2, MemBytes: 4 * GiB, Pids: 2048}, true
	case contract.BoxSizeMedium:
		return contract.BoxSize{Name: name, CPUs: 4, MemBytes: 8 * GiB, Pids: 4096}, true
	case contract.BoxSizeLarge:
		return contract.BoxSize{Name: name, CPUs: 8, MemBytes: 16 * GiB, Pids: 8192}, true
	case contract.BoxSizeXLarge:
		return contract.BoxSize{Name: name, CPUs: 16, MemBytes: 32 * GiB, Pids: 16384}, true
	default:
		return contract.BoxSize{}, false
	}
}
func diskRequest(name contract.BoxSizeName) int64 {
	switch name {
	case contract.BoxSizeAgent:
		return 5 * GiB
	case contract.BoxSizeSmall:
		return 10 * GiB
	case contract.BoxSizeMedium:
		return 20 * GiB
	case contract.BoxSizeLarge:
		return 40 * GiB
	case contract.BoxSizeXLarge:
		return 80 * GiB
	}
	return 0
}

func (b Box) GPUDevice() *int {
	if b.GPU == 0 {
		return nil
	}
	return &b.GPUCard
}

// HoldsGPU says the box takes VRAM on its card now: it asked for a GPU and its container can run. A box that waits, has stopped,
// failed or been deleted holds none.
func (b Box) HoldsGPU() bool {
	return b.GPU > 0 && b.State != contract.BoxPending && b.State != contract.BoxStopped && b.State != contract.BoxFailed && b.State != contract.BoxDeleted
}
