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

type View interface {
	OpenTerminal(contract.SessionID) bool
	Connected(contract.SessionID) bool
	SessionClosed(contract.SessionID)
}
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
}
