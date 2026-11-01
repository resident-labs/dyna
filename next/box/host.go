package box

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

type Slice struct {
	Name            string
	CPUs            int
	MemHigh, MemMax int64
	Pids            int
}
type Mount struct {
	HostPath, BoxPath string
	ReadOnly          bool
	MakeDir           bool
}
type EnvVar struct{ Name, Value string }
type Label struct{ Key, Value string }
type ContainerSpec struct {
	Box          contract.BoxID
	Image, Slice string
	Needs        []Need
	Mounts       []Mount
	Env          []EnvVar
	Labels       []Label
	// Pod is the pod the box joins, which then sets its user namespace and network. Empty for a box alone.
	Pod string
	// Volumes are its named compose volumes.
	Volumes []Volume
}

// ContainerLayout identifies the mount layout created by the current engine.
const ContainerLayout = "3"

type ContainerStatus struct {
	Box         contract.BoxID
	Running     bool
	StaleLayout bool
	// Stack is the Hash of the stack the container was made for.
	Stack string
}
type Forward struct {
	Box                  contract.BoxID
	HostPath, EnginePath string
}

// RunnerSlots is how many runners one host's proxy can tell apart. Each slot is
// one socket, and a runner mounts only its own.
const RunnerSlots = 16

// EgressSocket is the socket's name inside a slot's directory.
const EgressSocket = "egress.sock"

// RunnerSpec is one ephemeral CI runner. Config is its single-job JIT config, a secret.
type RunnerSpec struct {
	Name, Image, Config string
	Repo                contract.RepoName
	ID                  int64 // GitHub's runner ID
	Slot                int
	Size                contract.BoxSize
}

// Runner is a runner container found on a host.
type Runner struct {
	Name    string
	Repo    contract.RepoName
	ID      int64
	Slot    int
	Running bool
}

type HostDriver interface {
	EnsureSlice(context.Context, Slice) error
	RemoveSlice(context.Context, string) error
	CreateContainer(context.Context, ContainerSpec) error
	StartContainer(context.Context, contract.BoxID) error
	StopContainer(context.Context, contract.BoxID) error
	RemoveContainer(context.Context, contract.BoxID) error
	RemoveVolume(context.Context, contract.BoxID) error
	RemovePod(context.Context, contract.BoxID) error
	RemoveStackVolumes(context.Context, contract.BoxID) error
	ListContainers(context.Context) ([]ContainerStatus, error)
	AddForward(context.Context, Forward) error
	RemoveForward(context.Context, Forward) error
}
