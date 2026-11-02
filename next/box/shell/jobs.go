// Package shell runs the box's shell jobs and serves them to Pi over MCP.
package shell

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
	"golang.org/x/sys/unix"
)

const (
	foregroundLimit = 30 * time.Second
	stopGrace       = 5 * time.Second
	outputLimit     = 1 << 20 // output kept per job; the buffer trims back to it at twice this size
	tailLimit       = 2000
	keepFinished    = 20
	retryStart      = time.Second
	retryMax        = 30 * time.Second
)

// process is one running command. Signal reaches its whole process group.
type process interface {
	Wait() (int, string)
	Signal(syscall.Signal)
	GroupAlive() bool // whether any process of the group is left, after the leader exited
}

type execProcess struct{ cmd *exec.Cmd }

func startBash(command, cwd, callID string, out io.Writer) (process, error) {
	cmd := exec.Command("bash", "-c", command)
	cmd.Env = append(os.Environ(), contract.ToolCallIDEnv+"="+callID)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second // a background child that keeps the pipe open must not hold the job
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd}, nil
}

func (p *execProcess) Wait() (int, string) {
	_ = p.cmd.Wait()
	status := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return -1, unix.SignalName(status.Signal())
	}
	return p.cmd.ProcessState.ExitCode(), ""
}

func (p *execProcess) GroupAlive() bool { return syscall.Kill(-p.cmd.Process.Pid, 0) == nil }

func (p *execProcess) Signal(sig syscall.Signal) { _ = syscall.Kill(-p.cmd.Process.Pid, sig) }

type job struct {
	id          contract.ShellJobID
	command     string
	description string
	started     time.Time
	proc        process
	done        chan struct{}

	mu            sync.Mutex
	output        []byte // the newest output
	base          int    // bytes dropped before output[0]
	read          int    // bytes already returned, counted from the start of all output
	owner         int
	ownerFinished bool
	exit          int
	end           *contract.ShellJobEnd
	finished      bool
	ended         time.Time
	background    bool
	stopped       bool
	skipping      bool // inside a line that was cut; drop it to its newline
}

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(p)
	if j.skipping {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			j.base += n
			return n, nil
		}
		j.base += i + 1
		p, j.skipping = p[i+1:], false
	}
	j.output = append(j.output, p...)
	if len(j.output) > 2*outputLimit {
		// Drop the oldest bytes through the next newline, so no token is left half-cut.
		// With no newline, drop everything and the rest of that line as well.
		cut := len(j.output) - outputLimit
		if i := bytes.IndexByte(j.output[cut:], '\n'); i >= 0 {
			cut += i + 1
		} else {
			cut, j.skipping = len(j.output), true
		}
		j.output = append(j.output[:0], j.output[cut:]...)
		j.base += cut
	}
	return n, nil
}

// unread returns the masked output not yet returned, and whether the job still runs.
func (j *job) unread() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	start := max(j.read, j.base) - j.base
	end := len(j.output)
	if !j.finished {
		// A token may continue in the next write, so a running job releases whole lines only.
		end = start + bytes.LastIndexByte(j.output[start:], '\n') + 1
	}
	text := Mask(string(j.output[start:end]))
	if dropped := j.base - j.read; dropped > 0 {
		text = fmt.Sprintf("[%d bytes of earlier output were dropped]\n%s", dropped, text)
	}
	j.read = j.base + end
	return text, !j.finished
}

func (j *job) status() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.finished {
		return "running"
	}
	switch j.end.Kind {
	case contract.ShellJobStopped:
		return "stopped"
	case contract.ShellJobSignaled:
		return "signal " + j.end.Signal
	default:
		return fmt.Sprintf("exit %d", j.exit)
	}
}

// Config sets what the tests replace.
type Config struct {
	Dir    string                                    // default working directory
	After  func(time.Duration) <-chan time.Time      // the clock
	Report func(contract.ShellJobEndedRequest) error // called when a background job ends; retried until it succeeds

	newID func() contract.ShellJobID
	start func(command, cwd, callID string, out io.Writer) (process, error)
	now   func() time.Time // the clock for a job's start and end
}

// Server owns the box's jobs. They outlive any Pi process.
type Server struct {
	cfg      Config
	closed   chan struct{}
	mu       sync.Mutex
	jobs     []*job
	viewNext int
}

func New(cfg Config) *Server {
	if cfg.newID == nil {
		cfg.newID = func() contract.ShellJobID {
			var b [6]byte
			_, _ = rand.Read(b[:])
			return contract.ShellJobID("job_" + hex.EncodeToString(b[:]))
		}
	}
	if cfg.start == nil {
		cfg.start = startBash
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Server{cfg: cfg, closed: make(chan struct{})}
}

func (s *Server) find(id contract.ShellJobID) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.id == id {
			return j, true
		}
	}
	return nil, false
}

// run starts a command and waits up to the foreground limit. A job still running then continues in the background.
func (s *Server) run(cancel <-chan struct{}, command, cwd, description, callID string) (*job, error) {
	if cwd == "" {
		cwd = s.cfg.Dir
	}
	j := &job{id: s.cfg.newID(), command: command, started: s.cfg.now(), owner: -1, done: make(chan struct{})}
	j.description = Mask(description)
	proc, err := s.cfg.start(command, cwd, callID, j)
	if err != nil {
		return nil, err
	}
	j.proc = proc
	s.mu.Lock()
	s.jobs = append(s.dropOldFinished(), j)
	s.mu.Unlock()
	go func() {
		code, signal := proc.Wait()
		j.mu.Lock()
		j.exit, j.finished, j.ended = code, true, s.cfg.now()
		j.end = &contract.ShellJobEnd{Kind: contract.ShellJobExited, Code: code}
		if signal != "" {
			j.end = &contract.ShellJobEnd{Kind: contract.ShellJobSignaled, Signal: signal}
		}
		if j.stopped {
			j.end = &contract.ShellJobEnd{Kind: contract.ShellJobStopped}
		}
		// Until the web understands stopped endings, do not wake on an explicit stop.
		report := j.background && !j.stopped
		j.mu.Unlock()
		close(j.done)
		if report {
			s.report(j)
		}
	}()
	select {
	case <-j.done:
		return j, nil
	case <-s.cfg.After(foregroundLimit):
	case <-cancel:
	}
	j.mu.Lock()
	j.background = !j.finished
	j.mu.Unlock()
	return j, nil
}

// dropOldFinished keeps the running jobs and the last few finished ones.
func (s *Server) dropOldFinished() []*job {
	kept := make([]*job, 0, len(s.jobs))
	finished := 0
	for i := len(s.jobs) - 1; i >= 0; i-- {
		j := s.jobs[i]
		select {
		case <-j.done:
			if finished++; finished > keepFinished {
				continue
			}
		default:
		}
		kept = append(kept, j)
	}
	slices.Reverse(kept)
	return kept
}

func (s *Server) report(j *job) {
	j.mu.Lock()
	text := Mask(string(j.output)) // mask first: cutting first could split a token
	tail := strings.ToValidUTF8(text[max(0, len(text)-tailLimit):], "")
	req := contract.ShellJobEndedRequest{Job: j.id, Description: j.description, End: j.end, ExitCode: j.exit, Tail: tail}
	j.mu.Unlock()
	for delay := retryStart; s.cfg.Report(req) != nil; delay = min(delay*2, retryMax) {
		select {
		case <-s.cfg.After(delay):
		case <-s.closed:
			return
		}
	}
}

// stop ends the job's process group: SIGTERM, then SIGKILL after the grace period.
func (s *Server) stop(j *job) {
	j.mu.Lock()
	if !j.finished {
		j.stopped = true
	}
	j.mu.Unlock()
	grace := s.cfg.After(stopGrace)
	j.proc.Signal(syscall.SIGTERM)
	select {
	case <-j.done:
		if !j.proc.GroupAlive() {
			return
		}
		<-grace // the leader exited but a child ignores SIGTERM
	case <-grace:
	}
	j.proc.Signal(syscall.SIGKILL)
	<-j.done
}

// Running names the jobs that have not ended. Each turn's output carries them, for the engine's list of what a session holds.
func (s *Server) Running() []contract.ShellJobID {
	s.mu.Lock()
	defer s.mu.Unlock()
	var running []contract.ShellJobID
	for _, j := range s.jobs {
		select {
		case <-j.done:
		default:
			running = append(running, j.id)
		}
	}
	slices.Sort(running)
	return running
}

// Close kills every job group, as the agent stops.
func (s *Server) Close() {
	close(s.closed)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.proc.Signal(syscall.SIGKILL)
	}
}

// View returns a fresh or incremental masked owner view without moving agent reads.
func (s *Server) View(reset bool) []contract.ShellJobOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []contract.ShellJobOutput
	budget := 256 << 10
	first := s.viewNext
	for i := range s.jobs {
		index := (first + i) % len(s.jobs)
		j := s.jobs[index]
		j.mu.Lock()
		if reset {
			j.owner = -1
		}
		if budget <= 0 {
			j.mu.Unlock()
			continue
		}
		start := max(j.owner, j.base) - j.base
		end := len(j.output)
		if !j.finished {
			end = bytes.LastIndexByte(j.output, '\n') + 1
		}
		fresh := j.owner < j.base || end-start > 64<<10
		if fresh {
			start = max(0, end-(64<<10))
			if start > 0 {
				if i := bytes.IndexByte(j.output[start:end], '\n'); i >= 0 {
					start += i + 1
				} else {
					start = end
				}
			}
		}
		if end < start {
			end = start
		}
		if fresh || end > start || j.ownerFinished != j.finished {
			command := Mask(j.command)
			command = strings.ToValidUTF8(command[:min(len(command), 4<<10)], "")
			result = append(result, contract.ShellJobOutput{Job: j.id, Description: j.description, End: j.end, Command: command, Running: !j.finished, ExitCode: j.exit, Text: Mask(string(j.output[start:end])), Reset: fresh,
				Started: j.started, Ended: j.ended})
			budget -= end - start + len(command) + 256 // JSON fields and job ID
			s.viewNext = (index + 1) % len(s.jobs)
		}
		j.owner, j.ownerFinished = j.base+end, j.finished
		j.mu.Unlock()
	}
	return result
}
