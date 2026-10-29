package localci

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
)

// The node executor moved to internal/jobs. This file keeps the names local CI
// grew up with, as thin aliases and delegations, so the runner and its tests
// read the same as before the move.

type (
	Command        = jobs.Command
	checkResources = jobs.Resources
	scopeLimits    = jobs.Limits
	tailBuffer     = jobs.TailBuffer
)

const (
	defaultCheckCPUs          = jobs.DefaultCPUs
	defaultContainerMemoryMiB = jobs.DefaultContainerMemoryMiB
	defaultHostMemoryMiB      = jobs.DefaultHostMemoryMiB
	hostTasksMax              = jobs.HostTasksMax
	tasksPerCPU               = jobs.TasksPerCPU
	// admissionWait bounds how long one check may wait in the node queue
	// before the run gives up on it, so a queued check never waits for ever.
	admissionWait = 55 * time.Minute
	// longCallTimeout bounds one attempt of a node call that can run long.
	longCallTimeout = 15 * time.Minute
)

// defaultScopeLimits bound a scope that is not a check, such as node setup or
// cleanup.
var defaultScopeLimits = jobs.DefaultLimits

// clampCheckResources bounds a check's request by the machine, as the jobs
// executor's clamp does for every job.
func clampCheckResources(check nodeCheck, node Node) checkResources {
	return jobs.ClampResources(check.host, check.cpus, check.memoryMiB, jobs.Facts{CPUs: node.CPUs, MemoryBytes: node.MemoryBytes})
}

// checkUnit is the name of the systemd unit that holds a check's script. The
// check's own scope, used by a host check and by the cleanup, has the same
// base name with the type .scope.
func checkUnit(runID string, id Suite) string {
	return "dyna-ci-" + runID + "-" + string(id) + "-run.service"
}

// detachedDir is the folder of a check's detached files in its run folder, so a
// run's removal takes them too.
func detachedDir(runDir string, id Suite) string {
	return path.Join(runDir, string(id), "detached")
}

// queue is the node admission queue this runner joins: the one queue that
// jobs and checks of every room share on a node.
func (r Runner) queue() jobs.Admission {
	a := jobs.Admission{DataDir: r.root(), Engine: r.Engine}
	if r.Node != nil {
		a.Machines = nodeSource{node: r.Node}
	}
	return a
}

// nodeSource serves the runner's own node to the admission queue, so the
// queue reads the node's probed capacity without the machine registry.
type nodeSource struct{ node *Node }

func (s nodeSource) Get(ctx context.Context, name string) (machines.Machine, error) {
	if name != s.node.Name {
		return machines.Machine{}, fmt.Errorf("machine %s: %w", name, os.ErrNotExist)
	}
	return s.machine(), nil
}

func (s nodeSource) GetLocal(ctx context.Context) (machines.Machine, error) {
	return s.machine(), nil
}

// machine turns the node into a machine record: its probed CPUs and memory,
// and a full disk when the last probe found the node under the disk floor.
func (s nodeSource) machine() machines.Machine {
	m := machines.Machine{Name: s.node.Name, Enabled: true, Capabilities: machines.Capabilities{CPUs: s.node.CPUs, MemTotal: s.node.MemoryBytes}}
	if s.node.LowDisk {
		m.Capabilities.Disk = machines.Disk{Total: 100, Free: 0}
	}
	return m
}

// exec builds the node executor of this runner.
func (r Runner) exec() jobs.Executor {
	x := jobs.Executor{Command: r.Command, Wait: r.wait, Clock: r.clock}
	if r.Node != nil {
		x.Node = jobs.Conn{Name: r.Node.Name, SSH: r.Node.SSH, SSHConnectTimeout: r.Node.SSHConnectTimeout}
	}
	return x
}

func (r Runner) nodeCommand(ctx context.Context, out, stderr io.Writer, script string) error {
	return r.exec().NodeCommand(ctx, out, stderr, script)
}

func (r Runner) retry(ctx context.Context, timeout time.Duration, attempt func(context.Context) error) error {
	return r.exec().Retry(ctx, timeout, attempt)
}
