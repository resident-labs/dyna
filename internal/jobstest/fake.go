// Package jobstest is the fake node that tests of the jobs executor and of
// local CI share: it answers the detached protocol (start, poll, stop) and
// runs each job as a goroutine, so a test chooses what the job prints, how it
// ends and when the connection drops, with no ssh, no shell and no sleep. The
// package imports no Dyna package, so the tests of jobs and of localci both
// use it without a cycle.
package jobstest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake is a node for a test. Calls that are not part of the detached protocol,
// such as a setup script, git and a report copy, go to Legacy.
//
// A job is started the way the executor starts it: Legacy is called with the
// program "systemd-run" (or "bash" for an unbounded unit) and the job's script
// as its last argument. It writes the job's log to its output and returns the
// job's exit error, so a test describes a job in one function.
// RunFunc runs one command; it is the signature of a jobs executor command.
type RunFunc func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error

// Protocol names the three detached protocol scripts, so the fake knows what
// it answers without importing the executor package that defines them.
type Protocol struct{ Start, Poll, Stop string }

type Fake struct {
	t      *testing.T
	legacy RunFunc
	proto  Protocol

	mu           sync.Mutex
	procs        map[string]*fakeProc
	folders      map[string]bool
	starts       []Start
	polls        int
	stops        int
	stopFailures int
	now          time.Time
	// Down makes every call fail like a dropped connection while it returns
	// true.
	Down func() bool
}

type fakeProc struct {
	log      []byte
	exited   bool
	code     int
	modified time.Time
	activity time.Time
	capped   bool
	cancel   context.CancelFunc
	vanished bool
}

// Start records one launch the machine received.
type Start struct {
	Dir, Unit, CPU, Memory, Tasks, Script string
}

// NewFake makes a fake node whose jobs run legacy.
func NewFake(t *testing.T, protocol Protocol, legacy RunFunc) *Fake {
	t.Helper()
	return &Fake{t: t, proto: protocol, legacy: legacy, procs: map[string]*fakeProc{}, folders: map[string]bool{}, now: time.Unix(1_000_000, 0)}
}

// CommandFunc is the command runner that talks to the fake.
func (m *Fake) CommandFunc() RunFunc { return m.command }

// WaitFunc pauses the fake's clock instead of sleeping.
func (m *Fake) WaitFunc() func(context.Context, time.Duration) error { return m.sleep }

// ClockFunc reads the fake's clock, which only its own waits advance.
func (m *Fake) ClockFunc() func() time.Time {
	return func() time.Time {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.now
	}
}

func (m *Fake) sleep(ctx context.Context, d time.Duration) error {
	m.mu.Lock()
	m.now = m.now.Add(d)
	m.mu.Unlock()
	runtime.Gosched()
	return ctx.Err()
}

// PollCount is how many polls the machine answered.
func (m *Fake) PollCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.polls
}

// StartCalls lists the launches the machine received.
func (m *Fake) StartCalls() []Start {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Start(nil), m.starts...)
}

func (m *Fake) command(ctx context.Context, dir string, in io.Reader, out, stderr io.Writer, name string, args ...string) error {
	script, positional, ok := DecodeNodeScript(name, args)
	if !ok {
		return m.legacy(ctx, dir, in, out, stderr, name, args...)
	}
	if m.Down != nil && m.Down() {
		return SSHDropError(m.t)
	}
	switch script {
	case m.proto.Start:
		return m.start(in, positional)
	case m.proto.Poll:
		return m.poll(out, positional)
	case m.proto.Stop:
		m.mu.Lock()
		m.stops++
		fail := m.stopFailures > 0
		if fail {
			m.stopFailures--
		}
		m.mu.Unlock()
		if fail {
			return errors.New("fake stop failure")
		}
		m.stop(positional[0])
		return nil
	}
	return m.legacy(ctx, dir, in, out, stderr, name, args...)
}

func (m *Fake) start(in io.Reader, pos []string) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	dir := pos[0]
	m.mu.Lock()
	m.starts = append(m.starts, Start{Dir: dir, Unit: pos[1], CPU: pos[2], Memory: pos[3], Tasks: pos[4], Script: string(body)})
	if _, started := m.procs[dir]; started {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(m.t.Context())
	proc := &fakeProc{cancel: cancel, modified: m.now}
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
		var exitErr *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exitErr):
			code = exitErr.ExitCode()
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

// Mkdir records that a start created a job folder without ever starting
// its unit, as a start process that died between the two does. A poll of
// such a folder reports gone: the folder is there, the unit is not.
func (m *Fake) Mkdir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.folders[dir] = true
}

// CapLogs makes every running fake job discard later output, as the node's
// retained-log cap does. The fake still runs and can end normally.
func (m *Fake) CapLogs() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, proc := range m.procs {
		proc.capped = true
	}
}

// VanishAll makes every unit disappear without an exit record, as a machine
// that rebooted does.
func (m *Fake) VanishAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, proc := range m.procs {
		proc.vanished = true
	}
}

// Remove deletes a job's folder, as an operator's cleanup would.
func (m *Fake) Remove(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.procs, dir)
}

// Vanish makes one unit disappear without an exit record.
func (m *Fake) Vanish(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proc := m.procs[dir]; proc != nil {
		proc.vanished = true
	}
}

// FailStops makes the next count stop calls fail without stopping the unit.
func (m *Fake) FailStops(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopFailures = count
}

// StopCount is how many stops the machine received.
func (m *Fake) StopCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stops
}

func (m *Fake) stop(dir string) {
	m.mu.Lock()
	proc := m.procs[dir]
	m.mu.Unlock()
	if proc != nil {
		proc.cancel()
	}
}

func (m *Fake) poll(out io.Writer, pos []string) error {
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
			state := "missing"
			if m.folders[dir] {
				state = "gone"
			}
			fmt.Fprintf(out, "DYNA_DETACH dir=%s state=%s code= size=0 quiet_s=0 bytes=0\n\n", dir, state)
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
		modified := proc.modified
		if !proc.activity.IsZero() {
			modified = proc.activity
		}
		quiet := time.Duration(0)
		if !modified.IsZero() && m.now.After(modified) {
			quiet = m.now.Sub(modified)
		}
		fmt.Fprintf(out, "DYNA_DETACH dir=%s state=%s code=%s size=%d quiet_s=%d bytes=%d\n%s\n", dir, state, code, size, quiet/time.Second, len(chunk), chunk)
	}
	return nil
}

// fakeLog is the output of a running fake job.
type fakeLog struct {
	m    *Fake
	proc *fakeProc
}

func (l fakeLog) Write(p []byte) (int, error) {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	if !l.proc.capped {
		l.proc.log = append(l.proc.log, p...)
	}
	if len(p) > 0 {
		l.proc.activity = l.m.now
		if !l.proc.capped {
			l.proc.modified = l.m.now
		}
	}
	return len(p), nil
}

// SSHDropError is the error an ssh client returns when the connection drops.
func SSHDropError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 255").Run()
	if err == nil {
		t.Fatal("the fixture did not fail")
	}
	return err
}

// DecodeNodeScript finds the script and the positional arguments of a call
// that runs bash -c on the node, directly or inside an ssh command line.
func DecodeNodeScript(name string, args []string) (script string, positional []string, ok bool) {
	words := args
	if name == "ssh" {
		words = UnquoteWords(args[len(args)-1])
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

// UnquoteWords splits a command line made of words that shellquote.Quote
// wrote.
func UnquoteWords(line string) []string {
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
