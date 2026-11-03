// Package shell runs the box's shell jobs and serves them to Dyna's Pi extension, whose bash and job tools they are.
package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
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
	statusLines     = 5    // output lines in a running job's status
	maxTimeout      = 7200 // seconds, as for a host run
)

// nextMark is the running time at which a job reports next: 15 and 30 minutes, then each hour.
func nextMark(ran time.Duration) time.Duration {
	for _, mark := range []time.Duration{15 * time.Minute, 30 * time.Minute} {
		if ran < mark {
			return mark
		}
	}
	return (ran/time.Hour + 1) * time.Hour
}

// process is one running command. Signal reaches its whole process group.
type process interface {
	Wait() (int, string)
	Signal(syscall.Signal)
	GroupAlive() bool // whether any process of the group is left, after the leader exited
}

type execProcess struct{ cmd *exec.Cmd }

func startBash(command, cwd, callID string, out io.Writer) (process, error) {
	// exec skips its own check of Dir when SysProcAttr is set, and leaves a bare "no such file". %q shows a zero-width character.
	if _, err := os.Stat(cwd); cwd != "" && err != nil {
		return nil, invalid(fmt.Sprintf("cwd %q does not exist", cwd))
	}
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

// hostProcess is one command on a host. The engine runs it and answers when it ends. A signal asks the engine to stop it,
// and SIGKILL also stops waiting for the answer. A call that only ends stops nothing, since the box's connection to the
// engine can drop while the run is fine.
type hostProcess struct {
	done     chan struct{}
	cancel   context.CancelFunc // ends the call
	stop     func()             // asks the engine to stop the run
	stopOnce sync.Once
	stopped  chan struct{} // closes once the stop was asked, or was not needed
	code     int
	refused  *contract.Error // the engine ran nothing
}

func (p *hostProcess) Wait() (int, string) {
	<-p.done
	return p.code, ""
}

func (p *hostProcess) GroupAlive() bool { return false }

func (p *hostProcess) Signal(sig syscall.Signal) {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			close(p.stopped)
		default:
			go func() {
				defer close(p.stopped)
				p.stop()
			}()
		}
	})
	if sig == syscall.SIGKILL {
		p.cancel()
	}
}

type job struct {
	id          contract.ShellJobID
	command     string
	description string
	host        contract.HostName
	started     time.Time
	now         func() time.Time
	proc        process
	done        chan struct{}

	mu            sync.Mutex
	output        []byte // the newest output
	lastOutput    time.Time
	base          int // bytes dropped before output[0]
	read          int // bytes already returned, counted from the start of all output
	owner         int
	ownerFinished bool
	exit          int
	end           *contract.ShellJobEnd
	finished      bool
	ended         time.Time
	background    bool
	aside         bool // the foreground wait ended because a message arrived for the session
	stopped       bool
	timedOut      int  // the timeout in seconds, when it stopped the job
	skipping      bool // inside a line that was cut; drop it to its newline
}

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(p)
	j.lastOutput = j.now()
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

// running reports the job at now, or false once it has ended or been stopped.
func (j *job) running(now time.Time) (contract.ShellJobRunningRequest, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished || j.stopped {
		return contract.ShellJobRunningRequest{}, false
	}
	req := contract.ShellJobRunningRequest{Job: j.id, Description: j.description, RanMinutes: int(now.Sub(j.started) / time.Minute)}
	if !j.lastOutput.IsZero() {
		quiet := int(now.Sub(j.lastOutput) / time.Minute)
		req.QuietMinutes = &quiet
		// A token may continue in the next write, so a running job releases whole lines only.
		lines := strings.Split(strings.TrimRight(Mask(string(j.output[:bytes.LastIndexByte(j.output, '\n')+1])), "\n"), "\n")
		text := strings.Join(lines[max(0, len(lines)-statusLines):], "\n")
		req.Tail = strings.ToValidUTF8(text[max(0, len(text)-tailLimit):], "")
	}
	return req, true
}

func (j *job) status() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.finished {
		return "running"
	}
	switch j.end.Kind {
	case contract.ShellJobStopped:
		if j.timedOut > 0 {
			return timeoutNote(j.timedOut)
		}
		return "stopped"
	case contract.ShellJobSignaled:
		return "signal " + j.end.Signal
	default:
		return fmt.Sprintf("exit %d", j.exit)
	}
}

// Run is one bash call.
type Run struct {
	Command     string            `json:"command"`
	Description string            `json:"description"`
	Cwd         string            `json:"cwd"` // absolute in the box; on a host, from its login folder
	CallID      string            `json:"callId"`
	Host        contract.HostName `json:"host"`    // empty: this box
	Timeout     int               `json:"timeout"` // seconds; 0: none in the box, the engine's default on a host
	ClientID    string            `json:"-"`       // set by the job server for a host run, which HostStop names
}

func timeoutNote(seconds int) string { return fmt.Sprintf("stopped after its %d s timeout", seconds) }

// Config sets what the tests replace.
type Config struct {
	After  func(time.Duration) <-chan time.Time      // the clock
	Report func(contract.ShellJobEndedRequest) error // called when a background job ends; retried until it succeeds
	// Running is called at each mark of a background job's running time. A failed call is not retried: the next mark says more.
	Running func(contract.ShellJobRunningRequest) error
	// Host runs a command on a host through the engine and returns its exit code. All its output comes as the run ends.
	// Ending the context ends only the call.
	Host func(context.Context, Run, io.Writer) (int, *contract.Error)
	// HostStop asks the engine to stop the run that Host started with that client ID.
	HostStop func(ctx context.Context, clientID string) error

	newID func() contract.ShellJobID
	start func(command, cwd, callID string, out io.Writer) (process, error)
	now   func() time.Time // the clock for a job's start and end
}

// Server owns the box's jobs. They outlive any Pi process.
type Server struct {
	cfg      Config
	closed   chan struct{}
	aside    chan struct{} // closes when a message arrives for the session, and StepAside replaces it
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
	return &Server{cfg: cfg, closed: make(chan struct{}), aside: make(chan struct{})}
}

// StepAside ends the foreground wait of every command running in the foreground, as its limit would, so the call returns
// and Pi reads the message that arrived. A job already in the background is unaffected.
func (s *Server) StepAside() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.aside)
	s.aside = make(chan struct{})
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

// startHost hands a command to the engine. A refusal also goes in the output, in case the call has moved to the background.
func (s *Server) startHost(r Run, out io.Writer) *hostProcess {
	var id [16]byte
	_, _ = rand.Read(id[:])
	r.ClientID = hex.EncodeToString(id[:])
	ctx, cancel := context.WithCancel(context.Background())
	p := &hostProcess{done: make(chan struct{}), cancel: cancel, stopped: make(chan struct{})}
	p.stop = func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		if err := s.cfg.HostStop(stopCtx, r.ClientID); err != nil {
			log.Printf("host run %s: stop: %v", r.ClientID, err)
		}
	}
	go func() {
		defer close(p.done)
		defer cancel()
		p.code, p.refused = s.cfg.Host(ctx, r, out)
		if p.refused != nil && ctx.Err() != nil {
			// The job was stopped, which ended the call; the engine refused nothing.
			p.code, p.refused = -1, nil
			return
		}
		if p.refused != nil {
			p.code = 1
			_, _ = fmt.Fprintln(out, refusal{*p.refused})
		}
	}()
	return p
}

// run starts a command and waits up to the foreground limit, or until a message arrives. A job still running then continues
// in the background.
func (s *Server) run(cancel <-chan struct{}, r Run) (*job, error) {
	j := &job{id: s.cfg.newID(), command: r.Command, host: r.Host, started: s.cfg.now(), now: s.cfg.now, owner: -1, done: make(chan struct{})}
	j.description = Mask(r.Description)
	var proc process
	if r.Host != "" {
		proc = s.startHost(r, j)
	} else {
		var err error
		if proc, err = s.cfg.start(r.Command, r.Cwd, r.CallID, j); err != nil {
			return nil, err
		}
	}
	j.proc = proc
	s.mu.Lock()
	s.jobs = append(s.dropOldFinished(), j)
	aside := s.aside
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
		// An explicit stop is reported silently: the engine drops the job from what the session holds and sends no wake.
		// A timeout wakes, as the agent did not see it.
		report := j.background
		j.mu.Unlock()
		close(j.done)
		if report {
			s.report(j)
		}
	}()
	if r.Host == "" && r.Timeout > 0 {
		go s.expire(j, r.Timeout) // the engine ends a host run at its timeout
	}
	stepped := false
	select {
	case <-j.done:
		if host, ok := proc.(*hostProcess); ok && host.refused != nil {
			s.forget(j)
			return nil, refusal{*host.refused}
		}
		return j, nil
	case <-s.cfg.After(foregroundLimit):
	case <-aside:
		stepped = true
	case <-cancel:
	}
	j.mu.Lock()
	j.background = !j.finished
	j.aside = stepped && j.background
	watch := j.background && s.cfg.Running != nil
	j.mu.Unlock()
	if watch {
		go s.watch(j)
	}
	return j, nil
}

// expire stops a job at its timeout, in the foreground or the background, as job stop would.
func (s *Server) expire(j *job, seconds int) {
	select {
	case <-s.cfg.After(time.Duration(seconds) * time.Second):
	case <-j.done:
		return
	case <-s.closed:
		return
	}
	j.mu.Lock()
	expired := !j.finished && !j.stopped
	if expired {
		j.stopped, j.timedOut = true, seconds
	}
	j.mu.Unlock()
	if expired {
		s.stop(j)
	}
}

// forget drops a job that never ran.
func (s *Server) forget(j *job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = slices.DeleteFunc(s.jobs, func(other *job) bool { return other == j })
}

// watch reports a background job at each mark of its running time, until it ends or is stopped.
func (s *Server) watch(j *job) {
	for {
		ran := s.cfg.now().Sub(j.started)
		select {
		case <-s.cfg.After(nextMark(ran) - ran):
		case <-j.done:
			return
		case <-s.closed:
			return
		}
		req, running := j.running(s.cfg.now())
		if !running {
			return
		}
		if err := s.cfg.Running(req); err != nil {
			log.Printf("shell job %s: status: %v", j.id, err)
		}
	}
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
	if j.timedOut > 0 {
		if tail != "" && !strings.HasSuffix(tail, "\n") {
			tail += "\n"
		}
		tail += "[" + timeoutNote(j.timedOut) + "]"
	}
	req := contract.ShellJobEndedRequest{Job: j.id, Description: j.description, End: j.end, ExitCode: j.exit, Tail: tail, Silent: j.stopped && j.timedOut == 0}
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

// Close kills every job group, as the agent stops. It waits for each host run's stop to reach the engine, which takes at
// most the stop grace period.
func (s *Server) Close() {
	close(s.closed)
	s.mu.Lock()
	var stops []chan struct{}
	for _, j := range s.jobs {
		j.proc.Signal(syscall.SIGKILL)
		if host, ok := j.proc.(*hostProcess); ok {
			stops = append(stops, host.stopped)
		}
	}
	s.mu.Unlock()
	for _, stopped := range stops {
		<-stopped
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
			result = append(result, contract.ShellJobOutput{Job: j.id, Description: j.description, Host: j.host, End: j.end, Command: command, Running: !j.finished, ExitCode: j.exit, Text: Mask(string(j.output[start:end])), Reset: fresh,
				Started: j.started, Ended: j.ended})
			budget -= end - start + len(command) + 256 // JSON fields and job ID
			s.viewNext = (index + 1) % len(s.jobs)
		}
		j.owner, j.ownerFinished = j.base+end, j.finished
		j.mu.Unlock()
	}
	return result
}
