package box

import (
	"context"
	"time"

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
	// OnMount names the mount that HostPath sits on. Under a mount that is down lies a plain empty folder, so the host makes
	// no folder there.
	OnMount string
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
const ContainerLayout = "6"

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

// RunnerSlots is how many runners one host holds at once. A slot is one socket of the host's proxy, which a runner mounts
// alone, and one build folder in the host's cache. A slot holds one running runner, so no two jobs share either.
const RunnerSlots = 16

// EgressSocket is the socket's name inside a slot's directory.
const EgressSocket = "egress.sock"

// RunnerSpec is one ephemeral CI runner. Config is its single-job JIT config, a secret. Cache is the host's cache folder,
// which its boxes mount at /box/cache, and BuildCache the folder of the socket of its compiler cache.
type RunnerSpec struct {
	Name, Image, Config string
	Repo                contract.RepoName
	ID                  int64 // GitHub's runner ID
	Slot                int
	Size                contract.BoxSize
	Cache, BuildCache   string
}

// RunnerLayer is a room's ci-runner image, which a host builds on top of the shared runner image. Context is the saved folder as a
// tar, and ID is its content id.
type RunnerLayer struct {
	Room    contract.RoomID
	ID      string
	Context []byte
}

// Runner is a runner container found on a host.
type Runner struct {
	Name    string
	Repo    contract.RepoName
	ID      int64
	Slot    int
	Size    contract.BoxSize
	Running bool
}

// BaseImage is the Run image that needs no repository: dyna-base itself.
const BaseImage = "base"

// RunLimit is the longest a subscription's Run may take.
const RunLimit = 30 * time.Minute

// RunEventFile holds the event that fired a Run, among its files. The command reads it on stdin.
const RunEventFile = ".dyna-event.json"

// RunSpec is one subscription Run. Image is BaseImage or an image folder of the room's default repository,
// never a registry reference, and a Run with no room has no shared folder and no repository. Event is the type of what fired
// it, with RunEventFile among Files, and a schedule's Run has neither.
type RunSpec struct {
	ID           contract.BoxID
	Subscription contract.SubscriptionID
	Host         contract.HostName
	Room         contract.RoomID
	Image        string
	Size         contract.BoxSize
	Command      []string
	Files        []contract.RunFile
	Event        contract.EventType
}

// RunContainer is a RunSpec with its image and its host paths resolved.
type RunContainer struct {
	ID, Image string
	Size      contract.BoxSize
	Command   []string
	Mounts    []Mount
	Event     contract.EventType
}

// RunStatus is what a host knows about a Run's container.
type RunStatus struct {
	Found, Running bool
	ExitCode       int
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
