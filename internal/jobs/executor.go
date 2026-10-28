// Package jobs runs one command on a machine, detached. It owns the node
// executor that local CI built: a command starts in its own folder on the
// node, survives SSH drops, engine restarts and the end of the session that
// submitted it, and reports its log and exit through one poll call per node.
package jobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/shellquote"
)

// Command runs one local command. Tests replace it.
type Command func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error

// Conn is the route to a node: empty SSH runs on this machine.
type Conn struct {
	Name string
	SSH  string
	// SSHConnectTimeout is how long an ssh to the node waits for the
	// connection. Zero means the registry default.
	SSHConnectTimeout time.Duration
}

// Executor runs commands on one node. Every call is short: the engine never
// holds one connection for the length of a job.
type Executor struct {
	// Node is the route to the machine that runs the work.
	Node Conn
	// Command runs a local program. Nil uses the process-guarded default.
	Command Command
	// Wait pauses between polls and between retries of a node call, and Clock
	// reads the time a retry measures an unreachable node by. Tests replace
	// both, so a drop proves itself without a real sleep.
	Wait  func(context.Context, time.Duration) error
	Clock func() time.Time
}

// RunCommand runs one local command in the agent's own cgroup. Cancellation
// kills its whole process group and bounds the pipe drain, so a shell that
// dies cannot leave a child timeout or docker holding the output pipe open
// and block the caller before its cleanup.
func RunCommand(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	c := processguard.Command(ctx, name, args...)
	c.Dir = dir
	tempEnv, removeTemp := scratch.NewRunTemp()
	defer removeTemp()
	c.Env = append(processguard.MetadataEnv(), tempEnv...)
	c.Stdin = in
	c.Stdout = out
	c.Stderr = stderr
	err := runAndReap(c)
	// A cancelled command reports the context, not the signal that stopped it.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// runAndReap runs cmd and cleans up after a command that exited while a child
// it started kept the output pipe open, which os/exec reports as ErrWaitDelay
// once cmd.WaitDelay passes. The command's group id is its own pid, so what is
// left of the group is killed here instead of outliving the caller.
func runAndReap(c *exec.Cmd) error {
	return runAndReapWith(c.Run, c, reapGroup)
}

// runAndReapWith is runAndReap with the run and the reaper given, so a test can
// prove the decision without a real wait deadline or a real child.
func runAndReapWith(run func() error, c *exec.Cmd, reap func(int) error) error {
	err := run()
	if errors.Is(err, exec.ErrWaitDelay) && c.Process != nil {
		_ = reap(c.Process.Pid)
	}
	return err
}

// reapGroup kills what is left of a command's process group, whose id is the
// command's own pid. Tests replace it to observe the pid without racing a
// zombie.
var reapGroup = func(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }

// Run runs one local command with the executor's Command.
func (x Executor) Run(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
	fn := x.Command
	if fn == nil {
		fn = RunCommand
	}
	return fn(ctx, dir, nil, out, out, name, args...)
}

// NodeExec runs a script on the node once, with positional arguments and
// input. The script is the argument of bash, and every call is short.
func (x Executor) NodeExec(ctx context.Context, in io.Reader, out, stderr io.Writer, script string, args ...string) error {
	fn := x.Command
	if fn == nil {
		fn = RunCommand
	}
	argv := []string{"-o", "pipefail", "-c", script}
	if len(args) > 0 {
		argv = append(append(argv, "dyna-ci"), args...)
	}
	name, argv := x.NodeProgram("bash", argv)
	return fn(ctx, "", in, out, stderr, name, argv...)
}

// NodeCommand runs a short, repeatable script on the node and retries it while
// the connection fails. Its output reaches out and stderr only when the call
// ends, so a retry never repeats half a reply.
func (x Executor) NodeCommand(ctx context.Context, out, stderr io.Writer, script string) error {
	var stdout, diagnostics bytes.Buffer
	err := x.Retry(ctx, longCallTimeout, func(ctx context.Context) error {
		stdout.Reset()
		diagnostics.Reset()
		return x.NodeExec(ctx, nil, &stdout, &diagnostics, script)
	})
	_, _ = out.Write(stdout.Bytes())
	_, _ = stderr.Write(diagnostics.Bytes())
	return err
}

// NodeProgram wraps a program in ssh when the node is another machine. The
// dial deadline is the machine's own connect timeout, the same one its probe
// uses, so a node that does not answer costs the same bounded wait everywhere
// instead of whatever an ambient ssh config happens to set. The executor also
// never shares a multiplexed connection: an alias with ControlMaster can hand
// a command a stream the shared master truncated after the remote exit status
// arrived, which ssh reports as success. Each command owns its end-to-end
// connection, so a transport death fails the command with a drop instead
// (D-166).
func (x Executor) NodeProgram(name string, args []string) (string, []string) {
	if x.Node.SSH == "" {
		return name, args
	}
	remote := shellquote.Quote(name)
	for _, arg := range args {
		remote += " " + shellquote.Quote(arg)
	}
	return "ssh", append([]string{"-o", "BatchMode=yes", "-o", machines.SSHConnectTimeoutOption(x.Node.SSHConnectTimeout), "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}, x.Node.SSH, remote)
}

// sleepContext waits for d or until the context ends, so a cancelled run stops
// waiting between ssh dial retries.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// TailBuffer keeps the end of what was written to it, so an error message never
// grows without bound.
type TailBuffer struct{ data []byte }

func (b *TailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 16384 {
		b.data = b.data[len(b.data)-16384:]
	}
	return n, nil
}
func (b *TailBuffer) String() string { return string(b.data) }

// engineStopping records that this process is stopping the engine, so a job
// this stop cancels is left on its node for the restarted engine instead of
// being torn down as a cancellation.
var engineStopping atomic.Bool

// MarkEngineStopping tells the executor that the engine is shutting down.
func MarkEngineStopping() { engineStopping.Store(true) }

// EngineStopping reports whether the engine is shutting down.
func EngineStopping() bool { return engineStopping.Load() }

// ClearEngineStopping forgets the mark, for a test that shares the process.
func ClearEngineStopping() { engineStopping.Store(false) }

// SetEngineStoppingForTest sets the mark directly, for a test of another
// package that shares the process.
func SetEngineStoppingForTest(stop bool) { engineStopping.Store(stop) }
