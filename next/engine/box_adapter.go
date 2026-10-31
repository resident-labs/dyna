package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// BoxSpec identifies the admitted allocation the host adapter must create.
type BoxSpec struct {
	Box     contract.BoxID
	Session contract.SessionID
	Host    contract.HostName
	Slice   string
	Size    contract.BoxSize
	Repos   []contract.BoxRepo
}

// BoxAdapter performs host work after the service records admission.
type BoxAdapter interface {
	TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error)
	Create(context.Context, BoxSpec) (string, error)
	Remove(context.Context, contract.BoxID) error
	Start(context.Context, contract.BoxID) error
	Stop(context.Context, contract.BoxID) error
	Recreate(context.Context, BoxSpec, string, string) error
}
