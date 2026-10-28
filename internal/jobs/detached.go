package jobs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A check never depends on one SSH connection. It has four steps on the node,
// the same for a local node and an SSH node:
//
//	start   one short call writes the check's script and launches it detached
//	poll    one short call per node returns the new log bytes and the exit record
//	        of every running check on that node
//	collect the report copy, which runs after the checks end
//	stop    one short call kills the check's unit, or its process group
//
// The check writes its log and exit.json in its own folder on the node. A call
// that fails on the transport is retried, and the check is not affected: it
// counts as lost only when the node stays unreachable for NodeLostBound, or when
// its unit is gone without an exit record.
const (
	// PollInterval is the least time between the end of one poll and the start of
	// the next. A slow route paces itself: a poll that takes 20 seconds is
	// followed by the next one right after the wait, never by a queue of polls.
	PollInterval = 2 * time.Second
	// retryWait is the pause between two attempts of a call that failed on the
	// transport. Each attempt is already bounded by the machine's connect timeout.
	retryWait = 5 * time.Second
	// NodeLostBound is how long a node may stay unreachable before the checks on
	// it count as lost.
	NodeLostBound = 3 * time.Minute
	// StopTimeout bounds the call that stops a cancelled check.
	StopTimeout = time.Minute
	// CallTimeout bounds one attempt of a short call: start, poll or stop. The
	// ssh client also gives up on a silent connection after about 45 seconds.
	CallTimeout = 2 * time.Minute
	// longCallTimeout bounds one attempt of a call that can run long, such as the
	// node setup and the report copy.
	longCallTimeout = 15 * time.Minute
)

// StartScript runs on the node. It takes the check's folder, its unit
// name and the limits of its unit ($3 to $5, empty for none), and reads the
// check's script from its input. A lock file serializes starts, and a start
// that finds its check running or finished does nothing, so a retry never
// starts a check twice. A folder left by a start that stopped half way has no
// running unit and no exit record, so the retry prepares it again.
//
// The wrapper writes the check's output to the log up to a limit and discards
// the rest, so one noisy check cannot fill the node's disk. The check's own
// exit markers go to a file of their own and are appended to the log only after
// the output has drained, so they are always the end of the log. The writer of
// the log runs in a session of its own, so the cleanup of a host check, which
// kills the rest of its process group, cannot cut the log off. A daemon that
// keeps the output open cannot hold the check: the wrapper waits a short time for
// the writer to drain, then ends it.
//
// A transient unit survives the SSH session only when the user manager does, so
// the script enables lingering first. Without a user manager, or without
// lingering, the check starts under setsid and nohup and runs without a unit.
const StartScript = `set -eu
d=$1; unit=$2; cpu=$3; mem=$4; tasks=$5
umask 077
mkdir -p "$(dirname "$d")"
exec 8>"$d.lock"
flock -w 60 8
alive() { systemctl --user is-active --quiet "$unit" 2>/dev/null || { [ -s "$d/pid" ] && kill -0 "$(cat "$d/pid")" 2>/dev/null; }; }
if [ -s "$d/exit.json" ] || alive; then exit 0; fi
rm -rf "$d"
mkdir "$d"
cat > "$d/run.sh"
: > "$d/log"
cat > "$d/wrap.sh" <<'WRAP'
export DYNA_CI_TRAILER="$1/trailer"
mkfifo "$1/out"
setsid bash -c 'head -c "${DYNA_CI_LOG_CAP:-134217728}" >> "$1/log"; if IFS= read -r -n 1 _; then printf "\n[... the rest of the output was discarded on the node ...]\n" >> "$1/log"; cat > /dev/null; fi' dyna-cap "$1" < "$1/out" &
writer=$!
exec 4> "$1/out"
bash -o pipefail "$1/run.sh" >&4 2>&1 </dev/null
code=$?
exec 4>&-
for i in $(seq 1 "${DYNA_CI_DRAIN_TENTHS:-50}"); do kill -0 "$writer" 2>/dev/null || break; sleep 0.1; done
kill -KILL -- "-$writer" 2>/dev/null || true
wait "$writer" 2>/dev/null || true
for i in $(seq 1 40); do kill -0 -- "-$writer" 2>/dev/null || break; sleep 0.05; done
if [ -f "$1/trailer" ]; then cat "$1/trailer" >> "$1/log"; fi
printf '{"exitCode":%s}\n' "$code" > "$1/exit.tmp" && mv "$1/exit.tmp" "$1/exit.json"
WRAP
# The user manager's PATH is not the owner's. A check ran in an SSH login
# shell before it ran detached, so it gets the PATH a login shell of the node
# gives: tools from mise, cargo, nix, brew or ~/.local/bin stay found. A login
# shell that prints a banner leaves the PATH on the last line.
login_path=$(timeout 10s "${SHELL:-/bin/sh}" -lc 'printf "\n%s" "$PATH"' </dev/null 2>/dev/null | tail -n 1 || true)
unitOK=0
if command -v systemd-run >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  user=$(id -un)
  if [ "$(loginctl show-user "$user" -p Linger --value 2>/dev/null || true)" = yes ] || loginctl enable-linger "$user" >/dev/null 2>&1; then unitOK=1; fi
fi
if [ "$unitOK" = 1 ]; then
  if [ -n "$cpu" ]; then set -- -p "CPUQuota=$cpu" -p "MemoryMax=$mem" -p "TasksMax=$tasks"; else set --; fi
  if [ -n "$login_path" ]; then set -- "$@" "--setenv=PATH=$login_path"; fi
  systemd-run --user --quiet --collect --unit "$unit" "$@" -- nice -n 10 bash "$d/wrap.sh" "$d" 8>&- || { rm -rf "$d"; exit 1; }
else
  if [ -n "$login_path" ]; then PATH=$login_path; export PATH; fi
  setsid nohup nice -n 10 bash "$d/wrap.sh" "$d" 8>&- >/dev/null 2>&1 </dev/null &
  echo $! > "$d/pid"
fi
`

// PollScript runs on the node. Its arguments are groups of three: a
// check's folder, its unit name and the offset of the log bytes the engine has.
// For each group it prints one header line, the new log bytes, and a newline.
// The state is read before the size, so a check that exited is reported with
// its whole log. One poll returns at most 256 KiB of log per check, so a check
// that prints a lot cannot make a poll on a slow route run for minutes.
const PollScript = `chunk=262144
while [ "$#" -ge 3 ]; do
  d=$1; unit=$2; off=$3; shift 3
  alive() { systemctl --user is-active --quiet "$unit" 2>/dev/null || { [ -s "$d/pid" ] && kill -0 "$(cat "$d/pid")" 2>/dev/null; }; }
  if [ ! -d "$d" ]; then state=missing
  elif [ -s "$d/exit.json" ]; then state=exited
  elif alive; then state=running
  elif [ -s "$d/exit.json" ]; then state=exited
  else state=gone; fi
  code=
  if [ "$state" = exited ]; then code=$(sed -n 's/.*"exitCode":\(-\{0,1\}[0-9]\{1,\}\).*/\1/p' "$d/exit.json" | head -n 1); fi
  size=$(wc -c < "$d/log" 2>/dev/null || echo 0); size=$((size + 0))
  n=$((size - off)); [ "$n" -gt 0 ] || n=0; [ "$n" -le "$chunk" ] || n=$chunk
  printf 'DYNA_DETACH dir=%s state=%s code=%s size=%s bytes=%s\n' "$d" "$state" "$code" "$size" "$n"
  if [ "$n" -gt 0 ]; then tail -c +"$((off + 1))" "$d/log" | head -c "$n"; fi
  printf '\n'
done
`

// StopScript runs on the node. It takes the check's folder and its unit
// name, asks the check to stop, and kills what is left.
const StopScript = UnitStoppedFunction + `d=$1; unit=$2
timeout 20s systemctl --user stop "$unit" >/dev/null 2>&1 || true
systemctl --user kill --signal=KILL "$unit" >/dev/null 2>&1 || true
systemctl --user reset-failed "$unit" >/dev/null 2>&1 || true
if [ -s "$d/pid" ]; then
  pid=$(cat "$d/pid")
  kill -TERM -- "-$pid" 2>/dev/null || true
  for i in 1 2 3 4 5 6 7 8 9 10; do kill -0 -- "-$pid" 2>/dev/null || break; sleep 1; done
  kill -KILL -- "-$pid" 2>/dev/null || true
  if kill -0 -- "-$pid" 2>/dev/null; then echo "check process group is still running" >&2; exit 1; fi
elif command -v systemctl >/dev/null 2>&1; then
  # The stop counts only when the user manager says the unit is down. A manager
  # that does not answer proves nothing, so that fails the call too.
  unit_stopped "$unit" || { echo "unit $unit is not confirmed stopped" >&2; exit 1; }
fi
`

// UnitStoppedFunction defines unit_stopped for a node script: it succeeds
// only when the user manager answers and reports the unit inactive or failed.
// A query that fails is not an answer, and a unit that is unknown is inactive.
const UnitStoppedFunction = `unit_stopped() { local s; s=$(timeout 10s systemctl --user show -p ActiveState --value "$1" 2>/dev/null) || return 2; case "$s" in inactive|failed) return 0 ;; *) return 1 ;; esac; }
`

// RunSpec is everything the node needs to run and find one check.
type RunSpec struct {
	// ID is the check the unit belongs to.
	ID string
	// Attach follows a job that an earlier engine already started: nothing is
	// launched, and the poll continues at Offset.
	Attach bool
	// Offset is where an attached job's log continues; a launched job starts
	// at 0.
	Offset int64
	// Dir is the check's folder on the node.
	Dir string
	// Unit is the name of the job's systemd unit.
	Unit string
	// Script is the job's script.
	Script string
	// Limits bound the unit. A job that bounds its own command passes nil.
	Limits *Limits
}

// DetachedExit is a job that exited with a status on the node.
type DetachedExit struct{ Code int }

func (e *DetachedExit) Error() string { return fmt.Sprintf("job exited with status %d", e.Code) }

// NodeLostError is a job the engine can no longer follow: its node stayed
// unreachable, or its unit is gone and left no exit record.
type NodeLostError struct {
	Node string
	// Gone is set when the node answered but the job has no unit and no exit
	// record; otherwise the node did not answer for After.
	Gone  bool
	After time.Duration
	Cause error
}

func (e *NodeLostError) Error() string {
	if e.Gone {
		return fmt.Sprintf("the check on CI machine %s ended without an exit record", e.Node)
	}
	return fmt.Sprintf("CI machine %s was unreachable for %s: %v", e.Node, e.After.Round(time.Second), e.Cause)
}

func (e *NodeLostError) Unwrap() error { return e.Cause }

// PermanentError marks a failure that a retry cannot fix, such as a corrupt
// archive that reached the engine whole.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// CommandExitStatus is the exit status err proves: a command's own, or a detached
// check's.
func CommandExitStatus(err error) (int, bool) {
	var detached *DetachedExit
	if errors.As(err, &detached) {
		return detached.Code, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), true
	}
	return 0, false
}

func (x Executor) now() time.Time {
	if x.Clock != nil {
		return x.Clock()
	}
	return time.Now()
}

func (x Executor) pause(ctx context.Context, d time.Duration) error {
	if x.Wait != nil {
		return x.Wait(ctx, d)
	}
	return sleepContext(ctx, d)
}

// transient reports a failure of the connection, which a retry may fix: ssh's own
// exit 255, or a command that never ran. A local node has no connection, and a
// script that exited with its own status is an answer.
func (x Executor) transient(err error) bool {
	if x.Node.SSH == "" {
		return false
	}
	var exit *exec.ExitError
	return !errors.As(err, &exit) || exit.ExitCode() == 255
}

// Retry runs attempt again while it fails on the connection. Each attempt has
// its own timeout. It gives up when the node has been unreachable for
// NodeLostBound, measured on the executor's clock, and a stopped context ends
// it at once.
func (x Executor) Retry(ctx context.Context, timeout time.Duration, attempt func(context.Context) error) error {
	var since time.Time
	for {
		// The unreachable window starts when the first failed attempt started, and
		// later attempts and waits only get what is left of it, so the node counts
		// as lost after NodeLostBound in all, not after that and one more attempt.
		started := x.now()
		bound := timeout
		if !since.IsZero() {
			bound = min(timeout, max(NodeLostBound-started.Sub(since), time.Second))
		}
		callCtx, cancel := context.WithTimeout(ctx, bound)
		err := attempt(callCtx)
		cancel()
		var permanent *PermanentError
		if err == nil || ctx.Err() != nil || errors.As(err, &permanent) || !x.transient(err) {
			return err
		}
		if since.IsZero() {
			since = started
		}
		now := x.now()
		after := now.Sub(since)
		if after >= NodeLostBound {
			return &NodeLostError{Node: x.Node.Name, After: after, Cause: err}
		}
		if waitErr := x.pause(ctx, min(retryWait, NodeLostBound-after)); waitErr != nil {
			return errors.Join(err, waitErr)
		}
	}
}

// StartDetached launches a check on the node. The launch is safe to repeat.
func (x Executor) StartDetached(ctx context.Context, spec RunSpec) error {
	args := []string{spec.Dir, spec.Unit, "", "", ""}
	if spec.Limits != nil {
		args[2] = strconv.Itoa(spec.Limits.CPUs*100) + "%"
		args[3] = SystemdMemory(spec.Limits.MemoryMiB)
		args[4] = strconv.Itoa(spec.Limits.TasksMax)
	}
	return x.Retry(ctx, CallTimeout, func(ctx context.Context) error {
		var stderr TailBuffer
		if err := x.NodeExec(ctx, strings.NewReader(spec.Script), io.Discard, &stderr, StartScript, args...); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil
	})
}

// StopDetached kills a check's unit, or its process group, on its own context so
// a cancelled run still stops what it started.
func (x Executor) StopDetached(ctx context.Context, spec RunSpec) error {
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), StopTimeout)
	defer cancel()
	return x.Retry(stop, CallTimeout, func(ctx context.Context) error {
		var stderr TailBuffer
		if err := x.NodeExec(ctx, nil, io.Discard, &stderr, StopScript, spec.Dir, spec.Unit); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil
	})
}

// watch is one running check the poller follows.
type watch struct {
	spec   RunSpec
	out    io.Writer
	offset int64
	done   chan error
	closed bool
}

// Poller follows every running job of one node with a single poll call at a
// time, so many jobs never open many connections over a route that takes
// seconds to dial.
type Poller struct {
	x Executor
	// Started is told about each job once it is launched, so the caller can
	// record it for a restarted engine. It fires before the launch, so a
	// crash at any point inside it leaves the record saying not launched.
	Started func(RunSpec)
	// Launched is told once the node accepted the unit: the start call
	// returned. The caller records it, so a restart can tell a launch the
	// crashed engine completed from one that died inside the start.
	Launched func(RunSpec)
	mu       sync.Mutex
	active   map[string]*watch
	kick     chan struct{}
	exited   chan struct{}
}

// NewPoller starts the poller of a run. The returned function stops it and waits
// until its last poll has ended.
func (x Executor) NewPoller(ctx context.Context) (*Poller, func()) {
	ctx, cancel := context.WithCancel(ctx)
	p := &Poller{x: x, active: map[string]*watch{}, kick: make(chan struct{}, 1), exited: make(chan struct{})}
	go func() {
		defer close(p.exited)
		p.loop(ctx)
	}()
	return p, func() {
		cancel()
		<-p.exited
	}
}

// Run starts a check on the node and follows it until it exits. It writes the
// check's log to out as the polls bring it, and returns nil for exit status 0, a
// *DetachedExit for another status, and a *NodeLostError for a lost check. A
// stopped context stops the check on the node and returns the context's error.
func (p *Poller) Run(ctx context.Context, spec RunSpec, out io.Writer) error {
	if !spec.Attach {
		// The intent is recorded before the launch, so a restarted engine finds a
		// check whose launch the engine stop cut off. A check that was never
		// launched has no folder, and the restarted run starts it afresh.
		if p.Started != nil {
			p.Started(spec)
		}
		if err := p.x.StartDetached(ctx, spec); err != nil {
			if ctx.Err() != nil {
				if !EngineStopping() {
					_ = p.x.StopDetached(ctx, spec)
				}
				return ctx.Err()
			}
			return fmt.Errorf("start check: %w", err)
		}
		if p.Launched != nil {
			p.Launched(spec)
		}
	}
	w := &watch{spec: spec, out: out, offset: spec.Offset, done: make(chan error, 1)}
	p.mu.Lock()
	p.active[spec.Dir] = w
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
	select {
	case err := <-w.done:
		return err
	case <-ctx.Done():
		p.release(w)
		// An engine stop leaves the check running for the restarted engine.
		if !EngineStopping() {
			p.drain(ctx, w)
			_ = p.x.StopDetached(ctx, spec)
		}
		return ctx.Err()
	}
}

// drain reads the log a stopped check wrote since the last poll, so the
// evidence of a cancelled check still reaches its log file. It is one
// best-effort call.
func (p *Poller) drain(ctx context.Context, w *watch) {
	last, cancel := context.WithTimeout(context.WithoutCancel(ctx), StopTimeout)
	defer cancel()
	frames, err := p.x.pollNode(last, []*watch{w})
	if err != nil {
		return
	}
	if frame, ok := frames[w.spec.Dir]; ok {
		_, _ = w.out.Write(frame.Payload)
	}
}

// release takes a check away from the poller. After it returns, the poller never
// writes to the check's output again.
func (p *Poller) release(w *watch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.closed = true
	delete(p.active, w.spec.Dir)
}

func (p *Poller) snapshot() []*watch {
	p.mu.Lock()
	defer p.mu.Unlock()
	batch := make([]*watch, 0, len(p.active))
	for _, w := range p.active {
		batch = append(batch, w)
	}
	return batch
}

func (p *Poller) loop(ctx context.Context) {
	for {
		batch := p.snapshot()
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-p.kick:
				continue
			}
		}
		frames, err := p.x.pollNode(ctx, batch)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.failAll(batch, err)
		} else {
			p.apply(batch, frames)
		}
		if p.x.pause(ctx, PollInterval) != nil {
			return
		}
	}
}

// failAll ends every polled check with err.
func (p *Poller) failAll(batch []*watch, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range batch {
		if !w.closed {
			p.finish(w, err)
		}
	}
}

// finish ends a check with its result. The caller holds the poller's lock.
func (p *Poller) finish(w *watch, err error) {
	w.closed = true
	delete(p.active, w.spec.Dir)
	w.done <- err
}

// apply writes the new log bytes of each check and ends the checks that exited
// or vanished once their whole log arrived.
func (p *Poller) apply(batch []*watch, frames map[string]Frame) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range batch {
		frame, ok := frames[w.spec.Dir]
		if !ok || w.closed {
			continue
		}
		if len(frame.Payload) > 0 {
			_, _ = w.out.Write(frame.Payload)
			w.offset += int64(len(frame.Payload))
		}
		if w.offset < frame.Size {
			continue
		}
		switch frame.Unit {
		case UnitExited:
			if frame.Code == 0 {
				p.finish(w, nil)
			} else {
				p.finish(w, &DetachedExit{Code: frame.Code})
			}
		case UnitGone, UnitMissing:
			// A check that was launched has a folder. Without it, and without an
			// exit record, nothing can say how the check ended.
			p.finish(w, &NodeLostError{Node: p.x.Node.Name, Gone: true})
		}
	}
}

type UnitState string

const (
	UnitRunning UnitState = "running"
	UnitExited  UnitState = "exited"
	UnitGone    UnitState = "gone"
	// StateMissing is a check whose folder does not exist: it was never launched.
	UnitMissing UnitState = "missing"
)

// Frame is what one poll learned about one job.
type Frame struct {
	Unit    UnitState
	Code    int
	Size    int64
	Payload []byte
}

var pollHeader = regexp.MustCompile(`^DYNA_DETACH dir=(\S+) state=(running|exited|gone|missing) code=(-?[0-9]*) size=([0-9]+) bytes=([0-9]+)$`)

// Poll asks the node about every job in specs with one call, and retries the
// call when the connection fails. The offset of each job's log bytes comes
// from offsets, keyed by the job's folder; a job with no entry is read from
// the start.
func (x Executor) Poll(ctx context.Context, specs []RunSpec, offsets map[string]int64) (map[string]Frame, error) {
	batch := make([]*watch, 0, len(specs))
	for _, spec := range specs {
		batch = append(batch, &watch{spec: spec, offset: offsets[spec.Dir]})
	}
	return x.pollNode(ctx, batch)
}

// pollNode asks the node about every check in the batch with one call, and
// retries the call when the connection fails.
func (x Executor) pollNode(ctx context.Context, batch []*watch) (map[string]Frame, error) {
	var args []string
	for _, w := range batch {
		args = append(args, w.spec.Dir, w.spec.Unit, strconv.FormatInt(w.offset, 10))
	}
	var frames map[string]Frame
	err := x.Retry(ctx, CallTimeout, func(ctx context.Context) error {
		var out bytes.Buffer
		var stderr TailBuffer
		if err := x.NodeExec(ctx, nil, &out, &stderr, PollScript, args...); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		parsed, err := ParsePoll(&out)
		if err != nil {
			// A reply cut short is a failed connection, and the offsets stand.
			return err
		}
		frames = parsed
		return nil
	})
	return frames, err
}

// ParsePoll reads the frames of one poll reply. A reply that does not parse
// whole is an error and advances nothing.
func ParsePoll(reply io.Reader) (map[string]Frame, error) {
	frames := map[string]Frame{}
	in := bufio.NewReader(reply)
	for {
		line, err := in.ReadString('\n')
		if err == io.EOF && line == "" {
			return frames, nil
		}
		if err != nil {
			return nil, fmt.Errorf("poll reply ended inside a header: %w", err)
		}
		match := pollHeader.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
		if match == nil {
			return nil, fmt.Errorf("unexpected poll header %q", line)
		}
		size, _ := strconv.ParseInt(match[4], 10, 64)
		n, _ := strconv.Atoi(match[5])
		frame := Frame{Unit: UnitState(match[2]), Size: size, Payload: make([]byte, n)}
		if match[3] != "" {
			frame.Code, _ = strconv.Atoi(match[3])
		}
		if _, err := io.ReadFull(in, frame.Payload); err != nil {
			return nil, fmt.Errorf("poll reply ended inside the log of %s: %w", match[1], err)
		}
		if end, err := in.ReadByte(); err != nil || end != '\n' {
			return nil, fmt.Errorf("poll reply for %s lacks its terminator", match[1])
		}
		frames[match[1]] = frame
	}
}
