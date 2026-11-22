package lifecycle

import (
	"context"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type BoxSpec struct {
	Box     contract.BoxID
	Session contract.SessionID
	Room    contract.RoomID
	Root    bool // the session has no parent, so its box mounts the root-only add-ons; the runtime sets it
	Host    contract.HostName
	Slice   string
	Size    contract.BoxSize
	Repos   []contract.BoxRepo
	Needs   []box.Need
	Stack   *box.Stack
	GPUCard *int // the card the box was assigned, nil when it asked for none
}

type BoxAdapter interface {
	MaintainHost(context.Context, contract.HostName) error
	Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error)
	TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error)
	// ExecArgv runs a command in a box with no terminal, so its stdin and stdout carry bytes as they are.
	ExecArgv(context.Context, contract.HostName, contract.BoxID, ...string) ([]string, error)
	Create(context.Context, BoxSpec) (string, error)
	CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error)
	Remove(context.Context, contract.BoxID) error
	Start(context.Context, contract.BoxID) error
	Stop(context.Context, contract.BoxID) error
	Recreate(context.Context, BoxSpec, string) error
}

// LeftoverCleanup describes confirmed physical work, never deletion permission.
type LeftoverCleanup uint8

const (
	LeftoverDeferred          LeftoverCleanup = iota // active current resources were not touched
	LeftoverContainersRemoved                        // runnable resources are gone; named work remains
	LeftoverComplete                                 // runnable resources and named volumes are confirmed gone
)

// RoomHosts removes what a deleted room left on hosts. Each removal is idempotent.
type RoomHosts interface {
	RemoveRoom(context.Context, contract.HostName, contract.RoomID) error
	// RemoveLeftoverBox distinguishes deferred work from confirmed container-only
	// cleanup. Only complete removal permits retiring the durable debt.
	RemoveLeftoverBox(context.Context, contract.HostName, contract.BoxID, string) (LeftoverCleanup, error)
	// ForgetBox drops the engine's side of a box left on an offline host.
	ForgetBox(contract.BoxID) error
}

// RoomData finishes a deleted room on the control plane.
type RoomData interface {
	RemoveRoomData(contract.RoomID) error
	RoomDeleted(contract.RoomID)
}

type View interface {
	OpenTerminal(contract.SessionID) bool
	Connected(contract.SessionID) bool
	Stale(contract.SessionID) bool // its connected box agent is older than the engine, or it was marked to restart
	SessionClosed(contract.SessionID)
	// BoxStopped hears that a box has stopped, once nothing but a new box agent can take a delivery for it.
	BoxStopped(context.Context, contract.SessionID)
	// CompactBeforeStop asks a box about to stop for being quiet to compact, and reports true while the stop should wait.
	CompactBeforeStop(context.Context, contract.SessionID) bool
}
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
}
