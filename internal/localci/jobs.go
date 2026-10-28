package localci

import (
	"context"
	"io"
	"path"
	"time"

	"dyna-agent/internal/jobs"
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
	// admissionWait matches the explicit queue bound passed to the shared host
	// gate.
	admissionWait = jobs.AdmissionWait
	// longCallTimeout bounds one attempt of a node call that can run long.
	longCallTimeout = 15 * time.Minute
)

// defaultScopeLimits bound a scope that is not a check, such as node setup or
// cleanup.
var defaultScopeLimits = jobs.DefaultLimits

// clampCheckResources bounds a check's request by the machine, as the jobs
// executor's clamp does for every job.
func clampCheckResources(check nodeCheck, node Node) checkResources {
	facts := jobs.Facts{Slots: node.Slots, CPUs: node.CPUs, MemoryBytes: node.MemoryBytes}
	return jobs.ClampResources(check.host, check.cpus, check.memoryMiB, facts)
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
