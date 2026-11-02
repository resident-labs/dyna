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
	Host    contract.HostName
	Slice   string
	Size    contract.BoxSize
	Repos   []contract.BoxRepo
	Needs   []box.Need
	Stack   *box.Stack
}

type BoxAdapter interface {
	MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error)
	Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error)
	TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error)
	Create(context.Context, BoxSpec) (string, error)
	CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error)
	Remove(context.Context, contract.BoxID) error
	Start(context.Context, contract.BoxID) error
	Stop(context.Context, contract.BoxID) error
	Recreate(context.Context, BoxSpec, string, string) error
}

// RoomHosts removes what a deleted room left on hosts. Each removal is idempotent.
type RoomHosts interface {
	RemoveRoom(context.Context, contract.HostName, contract.RoomID) error
	RemoveLeftoverBox(context.Context, contract.HostName, contract.BoxID, string) error
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
	SessionClosed(contract.SessionID)
	// CompactBeforeStop asks a box about to stop for being quiet to compact, and reports true while the stop should wait.
	CompactBeforeStop(context.Context, contract.SessionID) bool
}
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
}
