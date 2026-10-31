package lifecycle

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

type BoxSpec struct {
	Box     contract.BoxID
	Session contract.SessionID
	Host    contract.HostName
	Slice   string
	Size    contract.BoxSize
	Repos   []contract.BoxRepo
}

type BoxAdapter interface {
	TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error)
	Create(context.Context, BoxSpec) (string, error)
	Remove(context.Context, contract.BoxID) error
	Start(context.Context, contract.BoxID) error
	Stop(context.Context, contract.BoxID) error
	Recreate(context.Context, BoxSpec, string, string) error
}

type View interface {
	OpenTerminal(contract.SessionID) bool
	Connected(contract.SessionID) bool
}
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
}
