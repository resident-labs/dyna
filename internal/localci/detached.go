package localci

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
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
// counts as lost only when the node stays unreachable for nodeLostBound, or when
// its unit is gone without an exit record.
const (
	// pollInterval is the least time between the end of one poll and the start of
	// the next. A slow route paces itself: a poll that takes 20 seconds is
	// followed by the next one right after the wait, never by a queue of polls.
	pollInterval = 2 * time.Second
	// retryWait is the pause between two attempts of a call that failed on the
	// transport. Each attempt is already bounded by the machine's connect timeout.
	retryWait = 5 * time.Second
	// nodeLostBound is how long a node may stay unreachable before the checks on
	// it count as lost.
	nodeLostBound = 3 * time.Minute
	// stopTimeout bounds the call that stops a cancelled check.
	stopTimeout = time.Minute
	// callTimeout bounds one attempt of a short call: start, poll or stop. The
	// ssh client also gives up on a silent connection after about 45 seconds.
	callTimeout = 2 * time.Minute
	// longCallTimeout bounds one attempt of a call that can run long, such as the
	// node setup and the report copy.
	longCallTimeout = 15 * time.Minute
)

// detachedStartScript runs on the node. It takes the check's folder, its unit
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
const detachedStartScript = `set -eu
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
unitOK=0
if command -v systemd-run >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  user=$(id -un)
  if [ "$(loginctl show-user "$user" -p Linger --value 2>/dev/null || true)" = yes ] || loginctl enable-linger "$user" >/dev/null 2>&1; then unitOK=1; fi
fi
if [ "$unitOK" = 1 ]; then
  if [ -n "$cpu" ]; then set -- -p "CPUQuota=$cpu" -p "MemoryMax=$mem" -p "TasksMax=$tasks"; else set --; fi
  systemd-run --user --quiet --collect --unit "$unit" "$@" -- nice -n 10 bash "$d/wrap.sh" "$d" 8>&- || { rm -rf "$d"; exit 1; }
else
  setsid nohup nice -n 10 bash "$d/wrap.sh" "$d" 8>&- >/dev/null 2>&1 </dev/null &
  echo $! > "$d/pid"
fi
`

// detachedPollScript runs on the node. Its arguments are groups of three: a
// check's folder, its unit name and the offset of the log bytes the engine has.
// For each group it prints one header line, the new log bytes, and a newline.
// The state is read before the size, so a check that exited is reported with
// its whole log. One poll returns at most 256 KiB of log per check, so a check
// that prints a lot cannot make a poll on a slow route run for minutes.
const detachedPollScript = `chunk=262144
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

// detachedStopScript runs on the node. It takes the check's folder and its unit
// name, asks the check to stop, and kills what is left.
const detachedStopScript = unitStoppedFunction + `d=$1; unit=$2
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

// unitStoppedFunction defines unit_stopped for a node script: it succeeds only
// when the user manager answers and reports the unit inactive or failed. A query
// that fails is not an answer, and a unit that is unknown is inactive.
const unitStoppedFunction = `unit_stopped() { local s; s=$(timeout 10s systemctl --user show -p ActiveState --value "$1" 2>/dev/null) || return 2; case "$s" in inactive|failed) return 0 ;; *) return 1 ;; esac; }
`

// detachSpec is everything the node needs to run and find one check.
type detachSpec struct {
	// ID is the check the unit belongs to.
	ID Suite
	// Attach follows a check that an earlier engine already started: nothing is
	// launched, and the poll brings its whole log.
	Attach bool
	// Dir is the check's folder on the node.
	Dir string
	// Unit is the name of the check's systemd unit.
	Unit string
	// Script is the check's script.
	Script string
	// Limits bound the unit. A host check passes nil: it bounds its own command.
	Limits *scopeLimits
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

// detachedExit is a check that exited with a status on the node.
type detachedExit struct{ Code int }

func (e *detachedExit) Error() string { return fmt.Sprintf("check exited with status %d", e.Code) }

// nodeLostError is a check the engine can no longer follow: its node stayed
// unreachable, or its unit is gone and left no exit record.
type nodeLostError struct {
	node string
	// gone is set when the node answered but the check has no unit and no exit
	// record; otherwise the node did not answer for after.
	gone  bool
	after time.Duration
	cause error
}

func (e *nodeLostError) Error() string {
	if e.gone {
		return fmt.Sprintf("the check on CI machine %s ended without an exit record", e.node)
	}
	return fmt.Sprintf("CI machine %s was unreachable for %s: %v", e.node, e.after.Round(time.Second), e.cause)
}

func (e *nodeLostError) Unwrap() error { return e.cause }

// permanentError marks a failure that a retry cannot fix, such as a corrupt
// archive that reached the engine whole.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// commandExitStatus is the exit status err proves: a command's own, or a detached
// check's.
func commandExitStatus(err error) (int, bool) {
	var detached *detachedExit
	if errors.As(err, &detached) {
		return detached.Code, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), true
	}
	return 0, false
}

func (r Runner) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

func (r Runner) pause(ctx context.Context, d time.Duration) error {
	if r.wait != nil {
		return r.wait(ctx, d)
	}
	return sleepContext(ctx, d)
}

// transient reports a failure of the connection, which a retry may fix: ssh's own
// exit 255, or a command that never ran. A local node has no connection, and a
// script that exited with its own status is an answer.
func (r Runner) transient(err error) bool {
	if r.Node.SSH == "" {
		return false
	}
	var exit *exec.ExitError
	return !errors.As(err, &exit) || exit.ExitCode() == 255
}

// retry runs attempt again while it fails on the connection. Each attempt has
// its own timeout. It gives up when the node has been unreachable for
// nodeLostBound, measured on the runner's clock, and a stopped context ends it
// at once.
func (r Runner) retry(ctx context.Context, timeout time.Duration, attempt func(context.Context) error) error {
	var since time.Time
	for {
		// The unreachable window starts when the first failed attempt started, and
		// later attempts and waits only get what is left of it, so the node counts
		// as lost after nodeLostBound in all, not after that and one more attempt.
		started := r.now()
		bound := timeout
		if !since.IsZero() {
			bound = min(timeout, max(nodeLostBound-started.Sub(since), time.Second))
		}
		callCtx, cancel := context.WithTimeout(ctx, bound)
		err := attempt(callCtx)
		cancel()
		var permanent *permanentError
		if err == nil || ctx.Err() != nil || errors.As(err, &permanent) || !r.transient(err) {
			return err
		}
		if since.IsZero() {
			since = started
		}
		now := r.now()
		after := now.Sub(since)
		if after >= nodeLostBound {
			return &nodeLostError{node: r.Node.Name, after: after, cause: err}
		}
		if waitErr := r.pause(ctx, min(retryWait, nodeLostBound-after)); waitErr != nil {
			return errors.Join(err, waitErr)
		}
	}
}

// startDetached launches a check on the node. The launch is safe to repeat.
func (r Runner) startDetached(ctx context.Context, spec detachSpec) error {
	args := []string{spec.Dir, spec.Unit, "", "", ""}
	if spec.Limits != nil {
		args[2] = strconv.Itoa(spec.Limits.CPUs*100) + "%"
		args[3] = systemdMemory(spec.Limits.MemoryMiB)
		args[4] = strconv.Itoa(spec.Limits.TasksMax)
	}
	return r.retry(ctx, callTimeout, func(ctx context.Context) error {
		var stderr tailBuffer
		if err := r.nodeExec(ctx, strings.NewReader(spec.Script), io.Discard, &stderr, detachedStartScript, args...); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil
	})
}

// stopDetached kills a check's unit, or its process group, on its own context so
// a cancelled run still stops what it started.
func (r Runner) stopDetached(ctx context.Context, spec detachSpec) error {
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	return r.retry(stop, callTimeout, func(ctx context.Context) error {
		var stderr tailBuffer
		if err := r.nodeExec(ctx, nil, io.Discard, &stderr, detachedStopScript, spec.Dir, spec.Unit); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		return nil
	})
}

// watch is one running check the poller follows.
type watch struct {
	spec   detachSpec
	out    io.Writer
	offset int64
	done   chan error
	closed bool
}

// detachedPoller follows every running check of one run on one node with a
// single poll call at a time, so nine checks never open nine connections over a
// route that takes seconds to dial.
type detachedPoller struct {
	r Runner
	// started is told about each check once it is launched, so the caller can
	// record it for a restarted engine.
	started func(detachSpec)
	mu      sync.Mutex
	active  map[string]*watch
	kick    chan struct{}
	exited  chan struct{}
}

// newPoller starts the poller of a run. The returned function stops it and waits
// until its last poll has ended.
func (r Runner) newPoller(ctx context.Context) (*detachedPoller, func()) {
	ctx, cancel := context.WithCancel(ctx)
	p := &detachedPoller{r: r, active: map[string]*watch{}, kick: make(chan struct{}, 1), exited: make(chan struct{})}
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
// *detachedExit for another status, and a *nodeLostError for a lost check. A
// stopped context stops the check on the node and returns the context's error.
func (p *detachedPoller) Run(ctx context.Context, spec detachSpec, out io.Writer) error {
	if !spec.Attach {
		// The intent is recorded before the launch, so a restarted engine finds a
		// check whose launch the engine stop cut off. A check that was never
		// launched has no folder, and the restarted run starts it afresh.
		if p.started != nil {
			p.started(spec)
		}
		if err := p.r.startDetached(ctx, spec); err != nil {
			if ctx.Err() != nil {
				if !EngineStopping() {
					_ = p.r.stopDetached(ctx, spec)
				}
				return ctx.Err()
			}
			return fmt.Errorf("start check: %w", err)
		}
	}
	w := &watch{spec: spec, out: out, done: make(chan error, 1)}
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
			_ = p.r.stopDetached(ctx, spec)
		}
		return ctx.Err()
	}
}

// drain reads the log a stopped check wrote since the last poll, so the
// evidence of a cancelled check still reaches its log file. It is one
// best-effort call.
func (p *detachedPoller) drain(ctx context.Context, w *watch) {
	last, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	frames, err := p.r.pollNode(last, []*watch{w})
	if err != nil {
		return
	}
	if frame, ok := frames[w.spec.Dir]; ok {
		_, _ = w.out.Write(frame.payload)
	}
}

// release takes a check away from the poller. After it returns, the poller never
// writes to the check's output again.
func (p *detachedPoller) release(w *watch) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.closed = true
	delete(p.active, w.spec.Dir)
}

func (p *detachedPoller) snapshot() []*watch {
	p.mu.Lock()
	defer p.mu.Unlock()
	batch := make([]*watch, 0, len(p.active))
	for _, w := range p.active {
		batch = append(batch, w)
	}
	return batch
}

func (p *detachedPoller) loop(ctx context.Context) {
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
		frames, err := p.r.pollNode(ctx, batch)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.failAll(batch, err)
		} else {
			p.apply(batch, frames)
		}
		if p.r.pause(ctx, pollInterval) != nil {
			return
		}
	}
}

// failAll ends every polled check with err.
func (p *detachedPoller) failAll(batch []*watch, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range batch {
		if !w.closed {
			p.finish(w, err)
		}
	}
}

// finish ends a check with its result. The caller holds the poller's lock.
func (p *detachedPoller) finish(w *watch, err error) {
	w.closed = true
	delete(p.active, w.spec.Dir)
	w.done <- err
}

// apply writes the new log bytes of each check and ends the checks that exited
// or vanished once their whole log arrived.
func (p *detachedPoller) apply(batch []*watch, frames map[string]pollFrame) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range batch {
		frame, ok := frames[w.spec.Dir]
		if !ok || w.closed {
			continue
		}
		if len(frame.payload) > 0 {
			_, _ = w.out.Write(frame.payload)
			w.offset += int64(len(frame.payload))
		}
		if w.offset < frame.size {
			continue
		}
		switch frame.state {
		case pollExited:
			if frame.code == 0 {
				p.finish(w, nil)
			} else {
				p.finish(w, &detachedExit{Code: frame.code})
			}
		case pollGone, pollMissing:
			// A check that was launched has a folder. Without it, and without an
			// exit record, nothing can say how the check ended.
			p.finish(w, &nodeLostError{node: p.r.Node.Name, gone: true})
		}
	}
}

type pollState string

const (
	pollRunning pollState = "running"
	pollExited  pollState = "exited"
	pollGone    pollState = "gone"
	// pollMissing is a check whose folder does not exist: it was never launched.
	pollMissing pollState = "missing"
)

// pollFrame is what one poll learned about one check.
type pollFrame struct {
	state   pollState
	code    int
	size    int64
	payload []byte
}

var pollHeader = regexp.MustCompile(`^DYNA_DETACH dir=(\S+) state=(running|exited|gone|missing) code=(-?[0-9]*) size=([0-9]+) bytes=([0-9]+)$`)

// pollNode asks the node about every check in the batch with one call, and
// retries the call when the connection fails.
func (r Runner) pollNode(ctx context.Context, batch []*watch) (map[string]pollFrame, error) {
	var args []string
	for _, w := range batch {
		args = append(args, w.spec.Dir, w.spec.Unit, strconv.FormatInt(w.offset, 10))
	}
	var frames map[string]pollFrame
	err := r.retry(ctx, callTimeout, func(ctx context.Context) error {
		var out bytes.Buffer
		var stderr tailBuffer
		if err := r.nodeExec(ctx, nil, &out, &stderr, detachedPollScript, args...); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		parsed, err := parsePoll(&out)
		if err != nil {
			// A reply cut short is a failed connection, and the offsets stand.
			return err
		}
		frames = parsed
		return nil
	})
	return frames, err
}

// parsePoll reads the frames of one poll reply. A reply that does not parse
// whole is an error and advances nothing.
func parsePoll(reply io.Reader) (map[string]pollFrame, error) {
	frames := map[string]pollFrame{}
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
		frame := pollFrame{state: pollState(match[2]), size: size, payload: make([]byte, n)}
		if match[3] != "" {
			frame.code, _ = strconv.Atoi(match[3])
		}
		if _, err := io.ReadFull(in, frame.payload); err != nil {
			return nil, fmt.Errorf("poll reply ended inside the log of %s: %w", match[1], err)
		}
		if end, err := in.ReadByte(); err != nil || end != '\n' {
			return nil, fmt.Errorf("poll reply for %s lacks its terminator", match[1])
		}
		frames[match[1]] = frame
	}
}
