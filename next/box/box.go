package box

import (
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type HostState string

const (
	HostChecking    HostState = "checking"
	HostReady       HostState = "ready"
	HostDraining    HostState = "draining"
	HostUnreachable HostState = "unreachable"
	HostRemoved     HostState = "removed"
)

type Host struct {
	EngineHost          *bool
	Name                contract.HostName
	SSH                 string
	State               HostState
	CPUs                int
	MemBytes, DiskBytes int64
	DiskFloor           int64
	MemUsed             int64 // latest heartbeat; not stored
	MaintainedAt        time.Time
	Labels              []string
	Rooms               []contract.RoomID
	CheckedAt           time.Time
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
}

type TransitionError struct{ From, To contract.BoxState }

func (e TransitionError) Error() string {
	return fmt.Sprintf("invalid box transition %s -> %s", e.From, e.To)
}

var transitions = map[contract.BoxState][]contract.BoxState{
	contract.BoxPending:  {contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxClosing},
	contract.BoxCreating: {contract.BoxPending, contract.BoxStarting, contract.BoxReady, contract.BoxFailed, contract.BoxClosing},
	contract.BoxStarting: {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
	contract.BoxReady:    {contract.BoxStopped, contract.BoxFailed, contract.BoxClosing, contract.BoxLost},
	contract.BoxStopped:  {contract.BoxStarting, contract.BoxPending, contract.BoxClosing, contract.BoxLost},
	contract.BoxClosing:  {contract.BoxDeleted, contract.BoxLost, contract.BoxFailed},
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
