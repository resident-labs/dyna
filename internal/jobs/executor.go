// Package jobs runs one command on a machine, detached. It owns the node
// executor that local CI built: a command starts in its own folder on the
// node, survives SSH drops, engine restarts and the end of the session that
// submitted it, and reports its log and exit through one poll call per node.
package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// remoteFrame runs only after the whole input arrived. The output trailer is
// emitted after the command and its byte count finish, not after ssh exits.
const remoteFrame = `set -u
tmp=$(mktemp -d) || exit 254
trap 'rm -rf "$tmp"' EXIT
IFS= read -r header || exit 254
case "$header" in "DYNA-IN "*) size=${header#DYNA-IN } ;; *) exit 254 ;; esac
case "$size" in *[!0-9]*|'') exit 254 ;; esac
[ "${#size}" = 20 ] || exit 254
size=$((10#$size))
head -c "$size" > "$tmp/in" || exit 254
[ "$(wc -c < "$tmp/in")" -eq "$size" ] || exit 254
script=$1; shift
mkfifo "$tmp/out" || exit 254
{
  cat "$tmp/out" >&3 & forward=$!
  n=$( { bash -o pipefail -c "$script" dyna-ci "$@" < "$tmp/in"; echo $? > "$tmp/status"; } | tee "$tmp/out" | wc -c )
  wait "$forward" || exit 254
} 3>&1
[ -s "$tmp/status" ] || exit 254
printf 'DYNA-END %020d %03d\n' "$n" "$(cat "$tmp/status")"
`

// NodeExec runs a script on the node once, with positional arguments and
// input. The script is the argument of bash, and every call is short.
func (x Executor) NodeExec(ctx context.Context, in io.Reader, out, stderr io.Writer, script string, args ...string) error {
	fn := x.Command
	if fn == nil {
		fn = RunCommand
	}
	if x.Node.SSH == "" {
		argv := []string{"-o", "pipefail", "-c", script}
		if len(args) > 0 {
			argv = append(append(argv, "dyna-ci"), args...)
		}
		return fn(ctx, "", in, out, stderr, "bash", argv...)
	}
	var input []byte
	var err error
	if in != nil {
		input, err = io.ReadAll(in)
		if err != nil {
			return err
		}
	}
	framedInput := io.MultiReader(strings.NewReader(fmt.Sprintf("DYNA-IN %020d\n", len(input))), bytes.NewReader(input))
	argv := append([]string{"-o", "pipefail", "-c", remoteFrame, "dyna-frame", script}, args...)
	name, argv := x.NodeProgram("bash", argv)
	frame := &trailerWriter{out: out}
	err = fn(ctx, "", framedInput, frame, stderr, name, argv...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if frame.err != nil {
		return frame.err
	}
	status, frameErr := frame.finish()
	if frameErr != nil {
		return frameErr
	}
	// Once the complete trailer arrived, SSH's exit status cannot contradict
	// the verified command status. Local write errors still matter.
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return err
	}
	if status != 0 {
		return &remoteExit{status}
	}
	return nil
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

// NodeProgram uses a Dyna-owned master, independent of the owner's SSH
// sharing settings. NodeExec verifies the reply trailer, so a master that
// dies after the remote status cannot turn a truncated reply into success
// (D-166).
func (x Executor) NodeProgram(name string, args []string) (string, []string) {
	if x.Node.SSH == "" {
		return name, args
	}
	remote := shellquote.Quote(name)
	for _, arg := range args {
		remote += " " + shellquote.Quote(arg)
	}
	return "ssh", append(x.sshOptions(), x.Node.SSH, remote)
}

// sshOptions gives git and node commands the same private control socket.
func (x Executor) sshOptions() []string {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		runtime = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	dir := filepath.Join(runtime, "dyna", "ssh")
	key := sha256.Sum256([]byte(x.Node.Name + "\x00" + x.Node.SSH))
	socket := filepath.Join(dir, fmt.Sprintf("%x.sock", key[:8]))
	if len(socket) >= 104 {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "dyna", "ssh")
		socket = filepath.Join(dir, fmt.Sprintf("%x.sock", key[:8]))
	}
	options := []string{"-o", "BatchMode=yes", "-o", machines.SSHConnectTimeoutOption(x.Node.SSHConnectTimeout)}
	// A host without a usable runtime folder must still be able to use SSH.
	// Explicitly disable sharing there, independent of the owner's config.
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		err = os.Chmod(dir, 0700)
	}
	if err != nil {
		options = append(options, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	} else {
		options = append(options, "-o", "ControlMaster=auto", "-o", "ControlPath="+socket, "-o", "ControlPersist=10m")
	}
	return append(options, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3")
}

const trailerLen = len("DYNA-END ") + 20 + 1 + 3 + 1

// trailerWriter streams all but the fixed-size trailer to its destination.
type trailerWriter struct {
	out   io.Writer
	tail  []byte
	count int64
	err   error
}

func (w *trailerWriter) Write(p []byte) (int, error) {
	n := len(p)
	data := append(w.tail, p...)
	if len(data) <= trailerLen {
		w.tail = data
		return n, nil
	}
	cut := len(data) - trailerLen
	written, err := w.out.Write(data[:cut])
	w.count += int64(written)
	w.tail = append(w.tail[:0], data[cut:]...)
	if err == nil && written != cut {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
		return 0, err
	}
	return n, nil
}

func (w *trailerWriter) finish() (int, error) {
	s := string(w.tail)
	if len(s) != trailerLen || !strings.HasPrefix(s, "DYNA-END ") || s[29] != ' ' || s[33] != '\n' {
		return 0, errors.New("node transport: missing or invalid reply trailer")
	}
	for _, c := range s[9:29] + s[30:33] {
		if c < '0' || c > '9' {
			return 0, errors.New("node transport: invalid reply trailer")
		}
	}
	n, err := strconv.ParseInt(s[9:29], 10, 64)
	if err != nil || n != w.count {
		return 0, errors.New("node transport: truncated reply")
	}
	status, _ := strconv.Atoi(s[30:33])
	if status > 255 {
		return 0, errors.New("node transport: invalid reply status")
	}
	return status, nil
}

type remoteExit struct{ code int }

func (e *remoteExit) Error() string { return fmt.Sprintf("node command exited with status %d", e.code) }

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
