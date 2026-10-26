package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMachine is a CI node for a test. It answers the detached protocol the
// runner speaks (start, poll, stop) and runs each check as a goroutine, so a
// test chooses what the check prints, how it ends and when the connection
// drops, with no ssh, no shell and no sleep. Calls that are not part of the
// protocol, such as the setup script, git and the report copy, go to legacy.
//
// A check is started the way the runner used to start it: legacy is called with
// the program "systemd-run" (or "bash" for an unbounded unit) and the check's
// script as its last argument. It writes the check's log to its output and
// returns the check's exit error, so a test describes a check in one function.
type fakeMachine struct {
	t      *testing.T
	legacy Command

	mu     sync.Mutex
	procs  map[string]*fakeProc
	starts []fakeStart
	polls  int
	stops  int
	now    time.Time
	// down makes every call fail like a dropped connection while it returns true.
	down func() bool
}

type fakeProc struct {
	log    []byte
	exited bool
	code   int
	cancel context.CancelFunc
	// vanished makes the unit disappear without an exit record.
	vanished bool
}

// fakeStart records one launch the machine received.
type fakeStart struct {
	Dir, Unit, CPU, Memory, Tasks, Script string
}

func newFakeMachine(t *testing.T, legacy Command) *fakeMachine {
	t.Helper()
	return &fakeMachine{t: t, legacy: legacy, procs: map[string]*fakeProc{}, now: time.Unix(1_000_000, 0)}
}

// attach makes r talk to the machine, on a clock that only the runner's own
// waits advance.
func (m *fakeMachine) attach(r *Runner) {
	r.Command = m.command
	r.wait = m.sleep
	r.clock = func() time.Time {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.now
	}
}

func (m *fakeMachine) sleep(ctx context.Context, d time.Duration) error {
	m.mu.Lock()
	m.now = m.now.Add(d)
	m.mu.Unlock()
	runtime.Gosched()
	return ctx.Err()
}

func (m *fakeMachine) pollCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.polls
}

func (m *fakeMachine) startCalls() []fakeStart {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fakeStart(nil), m.starts...)
}

func (m *fakeMachine) command(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	script, positional, ok := decodeNodeScript(name, args)
	if !ok {
		return m.legacy(ctx, dir, in, out, stderr, name, args...)
	}
	if m.down != nil && m.down() {
		return sshDropErrorFor(m.t)
	}
	switch script {
	case detachedStartScript:
		return m.start(in, positional)
	case detachedPollScript:
		return m.poll(out, positional)
	case detachedStopScript:
		m.stop(positional[0])
		return nil
	}
	return m.legacy(ctx, dir, in, out, stderr, name, args...)
}

func (m *fakeMachine) start(in io.Reader, pos []string) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	dir := pos[0]
	m.mu.Lock()
	m.starts = append(m.starts, fakeStart{Dir: dir, Unit: pos[1], CPU: pos[2], Memory: pos[3], Tasks: pos[4], Script: string(body)})
	if _, started := m.procs[dir]; started {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(m.t.Context())
	proc := &fakeProc{cancel: cancel}
	m.procs[dir] = proc
	m.mu.Unlock()
	callName, callArgs := "bash", []string{"-o", "pipefail", "-c", string(body)}
	if pos[2] != "" {
		callName = "systemd-run"
		callArgs = []string{"--user", "--scope", "--quiet", "--unit", pos[1], "-p", "CPUQuota=" + pos[2], "-p", "MemoryMax=" + pos[3], "-p", "TasksMax=" + pos[4], "--", "nice", "-n", "10", "bash", "-o", "pipefail", "-c", string(body)}
	}
	go func() {
		log := fakeLog{m: m, proc: proc}
		err := m.legacy(ctx, "", nil, log, log, callName, callArgs...)
		code := 0
		switch status, hasStatus := commandExitStatus(err); {
		case err == nil:
		case hasStatus:
			code = status
		case errors.Is(err, context.Canceled):
			code = 143
		default:
			code = 1
		}
		m.mu.Lock()
		proc.exited, proc.code = true, code
		m.mu.Unlock()
	}()
	return nil
}

// vanishAll makes every unit disappear without an exit record, as a machine
// that rebooted does.
func (m *fakeMachine) vanishAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, proc := range m.procs {
		proc.vanished = true
	}
}

func (m *fakeMachine) stopCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stops
}

func (m *fakeMachine) stop(dir string) {
	m.mu.Lock()
	m.stops++
	proc := m.procs[dir]
	m.mu.Unlock()
	if proc != nil {
		proc.cancel()
	}
}

func (m *fakeMachine) poll(out io.Writer, pos []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.polls++
	for i := 0; i+2 < len(pos); i += 3 {
		dir := pos[i]
		offset, err := strconv.Atoi(pos[i+2])
		if err != nil {
			return err
		}
		proc := m.procs[dir]
		if proc == nil {
			fmt.Fprintf(out, "DYNA_DETACH dir=%s state=gone code= size=0 bytes=0\n\n", dir)
			continue
		}
		state, code := "running", ""
		if proc.vanished {
			state = "gone"
		} else if proc.exited {
			state, code = "exited", strconv.Itoa(proc.code)
		}
		size := len(proc.log)
		chunk := proc.log[min(offset, size):min(size, offset+256<<10)]
		fmt.Fprintf(out, "DYNA_DETACH dir=%s state=%s code=%s size=%d bytes=%d\n%s\n", dir, state, code, size, len(chunk), chunk)
	}
	return nil
}

// fakeLog is the output of a running fake check.
type fakeLog struct {
	m    *fakeMachine
	proc *fakeProc
}

func (l fakeLog) Write(p []byte) (int, error) {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	l.proc.log = append(l.proc.log, p...)
	return len(p), nil
}

// sshDropErrorFor is the error an ssh client returns when the connection drops.
func sshDropErrorFor(t *testing.T) error { return sshDropError(t) }

// decodeNodeScript finds the script and the positional arguments of a call that
// runs bash -c on the node, directly or inside an ssh command line.
func decodeNodeScript(name string, args []string) (script string, positional []string, ok bool) {
	words := args
	if name == "ssh" {
		words = unquoteWords(args[len(args)-1])
		name = ""
		if len(words) > 0 {
			name, words = words[0], words[1:]
		}
	}
	if name != "bash" || len(words) < 3 || words[0] != "-o" || words[2] != "-c" {
		return "", nil, false
	}
	// bash -o pipefail -c SCRIPT [dyna-ci ARG...]
	if len(words) < 4 {
		return "", nil, false
	}
	if len(words) > 5 {
		positional = words[5:]
	}
	return words[3], positional, true
}

// unquoteWords splits a command line made of words that shellquote.Quote wrote.
func unquoteWords(line string) []string {
	var words []string
	for i := 0; i < len(line); {
		if line[i] == ' ' {
			i++
			continue
		}
		var word strings.Builder
		for i < len(line) && line[i] != ' ' {
			switch line[i] {
			case '\'':
				end := strings.IndexByte(line[i+1:], '\'')
				if end < 0 {
					return words
				}
				word.WriteString(line[i+1 : i+1+end])
				i += end + 2
			case '\\':
				if i+1 < len(line) {
					word.WriteByte(line[i+1])
				}
				i += 2
			default:
				word.WriteByte(line[i])
				i++
			}
		}
		words = append(words, word.String())
	}
	return words
}
