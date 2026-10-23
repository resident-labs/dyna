package sessionhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/processguard"
)

const (
	// idleClose stops a session process after this much time with no turn
	// attached and no output, so idle sessions hold no memory. The next turn
	// resumes the same session file in a fresh process.
	localIdleClose = 10 * time.Minute
	// localStopGrace bounds the orderly exit after stdin closes; a
	// straggler is killed.
	localStopGrace = 5 * time.Second
	// localTerminateGrace bounds the wait after a group SIGTERM before the
	// group is killed.
	localTerminateGrace = 250 * time.Millisecond
	// stderrTailLimit bounds the stderr one child keeps and the diagnostics a
	// session error carries.
	stderrTailLimit = 8 << 10
)

// Local returns a fresh in-process host: the same supervision the detached
// host process runs, without the process. Tests use it; production adapters
// use Default.
func Local() Host { return newLocalHost() }

// localHost owns one session process per key. The detached host process runs
// one of these for its single key; Local uses one for tests.
type localHost struct {
	mu       sync.Mutex
	sessions map[string]*localSession
	// onEmpty runs, outside mu, after a session entry is dropped.
	onEmpty func()
}

func newLocalHost() *localHost {
	return &localHost{sessions: map[string]*localSession{}}
}

// Session returns the live process for launch's key, starting or replacing
// one as the fingerprint demands, and attaches the calling turn. A key
// already attached to another turn is refused before anything is replaced.
func (h *localHost) Session(ctx context.Context, launch Launch) (Session, error) {
	if launch.Key == "" || len(launch.Argv) == 0 {
		return nil, errors.New("session launch needs a key and a command line")
	}
	id := launch.id()
	h.mu.Lock()
	defer h.mu.Unlock()
	s, found := h.sessions[id]
	if found && !s.beginTurn() {
		return nil, fmt.Errorf("session %s already has an attached turn", launch.Key)
	}
	if !found {
		s = &localSession{key: id, host: h, activity: make(chan struct{}, 1)}
		s.beginTurn()
	}
	if found {
		if c := s.liveChild(launch.Fingerprint); c != nil {
			if launch.BindLease != nil {
				if err := launch.BindLease(c.guardRef); err != nil {
					s.Detach(DetachIdle)
					return nil, err
				}
			}
			return s, nil
		}
	}
	if err := s.Replace(ctx, launch); err != nil {
		delete(h.sessions, id)
		s.stopIdleTimer()
		return nil, err
	}
	h.sessions[id] = s
	return s, nil
}

// drop removes a session entry whose child ended, so the next turn starts a
// replacement.
func (h *localHost) drop(s *localSession) {
	h.mu.Lock()
	if h.sessions[s.key] == s {
		delete(h.sessions, s.key)
	}
	h.mu.Unlock()
	if h.onEmpty != nil {
		h.onEmpty()
	}
}

func (h *localHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// closeAll stops every entry's child.
func (h *localHost) closeAll() {
	h.mu.Lock()
	all := make([]*localSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		all = append(all, s)
	}
	h.mu.Unlock()
	for _, s := range all {
		_ = s.Close()
	}
}

// child is one spawned harness process owned by a session. Replace swaps
// the child inside the same session entry. processguard is its only
// supervisor: spawn, wait, signal and liveness reference.
type child struct {
	stdin      io.WriteCloser
	controller *processguard.Controller
	guardRef   processguard.Reference
	exited     chan struct{}
	stderr     stderrWindow
	stderrPipe *stderrPipe
}

// stderrWindow keeps the last stderrTailLimit bytes a child wrote to stderr
// and counts every byte, so a turn can ask for the bytes written since its
// attach without the buffer growing for the life of the process.
type stderrWindow struct {
	mu    sync.Mutex
	buf   []byte
	total int
	since int
}

func (w *stderrWindow) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(p)
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*stderrTailLimit {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-stderrTailLimit:]...)
	}
	return len(p), nil
}

// markTurn starts a new per-turn window.
func (w *stderrWindow) markTurn() {
	w.mu.Lock()
	w.since = w.total
	w.mu.Unlock()
}

// tail returns the bytes written since the last markTurn, at most
// stderrTailLimit of them.
func (w *stderrWindow) tail(sinceStart bool) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.buf)
	if !sinceStart && w.total-w.since < n {
		n = w.total - w.since
	}
	if n > stderrTailLimit {
		n = stderrTailLimit
	}
	return string(w.buf[len(w.buf)-n:])
}

// localSession is one session's long-lived child. The exec copier feeds
// stdout frames to route; they buffer for the attached turn and drop between
// turns. Command ids and response correlation are the codec's job.
type localSession struct {
	key      string
	host     *localHost
	activity chan struct{}

	mu          sync.Mutex
	child       *child
	last        *child // the newest child, kept after it exits for its stderr
	fingerprint string
	exitErr     error // the last child's exit, once the session has no child
	events      [][]byte
	turn        bool
	idleTimer   *time.Timer
}

// lineWriter splits one child's stdout strictly on LF, strips one trailing
// CR, and hands complete frames to route. Protocol frames are LF-delimited
// only; no other Unicode separator ends a frame.
type lineWriter struct {
	s       *localSession
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSuffix(w.pending[:i], []byte{'\r'})
		if len(line) > 0 {
			w.s.route(append([]byte(nil), line...))
		}
		w.pending = w.pending[i+1:]
	}
	w.pending = append([]byte(nil), w.pending...)
	return len(p), nil
}

// route buffers one stdout frame for the attached turn; between turns the
// frame is dropped.
func (s *localSession) route(line []byte) {
	s.markActive()
	s.mu.Lock()
	if s.turn {
		s.events = append(s.events, line)
		s.mu.Unlock()
		s.wake()
		return
	}
	s.mu.Unlock()
}

func (s *localSession) wake() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func (s *localSession) markActive() {
	s.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Reset(localIdleClose)
	}
	s.mu.Unlock()
}

func (s *localSession) stopIdleTimer() {
	s.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.mu.Unlock()
}

// beginTurn marks the session attached and starts a fresh frame buffer and
// stderr window. It reports false when another turn already holds it.
func (s *localSession) beginTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn {
		return false
	}
	s.turn = true
	s.events = nil
	if s.last != nil {
		s.last.stderr.markTurn()
	}
	return true
}

// Write sends one protocol frame to the child's stdin.
func (s *localSession) Write(frame []byte) error {
	s.mu.Lock()
	c := s.child
	s.mu.Unlock()
	if c == nil {
		return errors.New("session has no child")
	}
	if _, err := c.stdin.Write(append(append([]byte(nil), frame...), '\n')); err != nil {
		return fmt.Errorf("write session frame: %w", err)
	}
	s.markActive()
	return nil
}

// Next returns the next buffered stdout frame, blocking until one arrives,
// the child exits, or ctx ends. Frames buffered before an exit are returned
// first; the exit error carries the child's wait error and bounded stderr.
func (s *localSession) Next(ctx context.Context) ([]byte, error) {
	for {
		s.mu.Lock()
		if len(s.events) > 0 {
			line := s.events[0]
			s.events = s.events[1:]
			s.mu.Unlock()
			return line, nil
		}
		c, exitErr := s.child, s.exitErr
		s.mu.Unlock()
		if c == nil {
			if exitErr != nil {
				return nil, exitErr
			}
			return nil, errors.New("session has no child")
		}
		select {
		case <-s.activity:
		case <-c.exited:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func exitError(err error, stderr string) error {
	text := "session process exited"
	if err != nil {
		text += ": " + err.Error()
	}
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		text += ": " + stderr
	}
	return errors.New(text)
}

// Replace stops the child and starts launch's in its place on the same
// session: a model change, account switch or updated bundle.
func (s *localSession) Replace(ctx context.Context, launch Launch) error {
	s.mu.Lock()
	old := s.child
	s.child = nil
	s.mu.Unlock()
	if old != nil {
		stopChild(old)
	}
	s.mu.Lock()
	s.events = nil
	s.mu.Unlock()
	return s.startChild(launch)
}

// Detach ends the turn's attachment. A clean detach keeps the child for the
// next turn. An abandoned one kills the child's group at once and drops the
// session, so no tool keeps running unwatched.
func (s *localSession) Detach(reason DetachReason) {
	s.mu.Lock()
	s.turn = false
	s.events = nil
	c := s.child
	if reason == DetachAbandoned {
		s.child = nil
		s.exitErr = errors.New("session process abandoned")
	}
	s.mu.Unlock()
	if reason == DetachAbandoned && c != nil {
		_ = c.controller.Signal(syscall.SIGKILL)
		s.stopIdleTimer()
		s.host.drop(s)
	}
}

// Close stops the child and drops the session. The next Session call starts
// a replacement.
func (s *localSession) Close() error {
	s.mu.Lock()
	c := s.child
	s.child = nil
	s.mu.Unlock()
	s.stopIdleTimer()
	if c != nil {
		stopChild(c)
	}
	s.host.drop(s)
	return nil
}

// Halt stops the child's process group the way a canceled turn does:
// terminate, grace, kill.
func (s *localSession) Halt() {
	s.mu.Lock()
	c := s.child
	s.mu.Unlock()
	if c == nil {
		return
	}
	if err := c.controller.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, processguard.ErrGroupEnded) {
		_ = c.controller.Signal(syscall.SIGKILL)
	}
	select {
	case <-c.exited:
	case <-time.After(localTerminateGrace):
		_ = c.controller.Signal(syscall.SIGKILL)
		<-c.exited
	}
}

// KillGroup immediately kills the child's process group.
func (s *localSession) KillGroup() {
	s.mu.Lock()
	c := s.child
	s.mu.Unlock()
	if c != nil {
		_ = c.controller.Signal(syscall.SIGKILL)
	}
}

// StderrTail drains pending pipe bytes before returning this turn's bounded stderr.
func (s *localSession) StderrTail() string {
	s.mu.Lock()
	c := s.last
	s.mu.Unlock()
	if c == nil {
		return ""
	}
	if c.stderrPipe != nil {
		c.stderrPipe.drain()
	}
	return c.stderr.tail(false)
}

// liveChild returns the live child when it still has this launch identity.
func (s *localSession) liveChild(fingerprint string) *child {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child != nil && s.fingerprint == fingerprint {
		return s.child
	}
	return nil
}

// stopChild ends one child: stdin closes first (an orderly shutdown), then a
// grace, then a group kill.
func stopChild(c *child) {
	_ = c.stdin.Close()
	select {
	case <-c.exited:
	case <-time.After(localStopGrace):
		_ = c.controller.Signal(syscall.SIGKILL)
		<-c.exited
	}
}

// idleClose stops a session with no turn attached that went quiet. A race
// with a fresh attach re-arms the timer instead.
func (s *localSession) idleClose() {
	s.mu.Lock()
	if s.turn || s.child == nil {
		if s.child != nil && s.idleTimer != nil {
			s.idleTimer.Reset(localIdleClose)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = s.Close()
}

// startChild spawns launch's child and supervises it through processguard,
// the one supervisor: the guard anchor owns the process group, BindLease
// runs while the bootstrap gate is closed, and the session keeps the guard
// reference for every later attach.
func (s *localSession) startChild(launch Launch) error {
	cmd := exec.Command(launch.Argv[0], launch.Argv[1:]...)
	cmd.Dir = launch.Dir
	cmd.Env = append([]string(nil), launch.Env...)
	// A descendant that escapes the group can hold the output pipes open
	// after the child exits; bound that drain, but not so far that an
	// orderly stop pays whole seconds.
	cmd.WaitDelay = 100 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	c := &child{stdin: stdin, exited: make(chan struct{})}
	cmd.Stdout = &lineWriter{s: s}
	c.stderrPipe, err = newStderrPipe(&c.stderr)
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stderr = c.stderrPipe.writer
	c.stderrPipe.start()
	started := false
	defer func() {
		_ = c.stderrPipe.writer.Close()
		if !started {
			c.stderrPipe.finish()
		}
	}()
	controller, err := processguard.Wrap(cmd)
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := controller.Start(func(ref processguard.Reference) error {
		if launch.BindLease == nil {
			return nil
		}
		return launch.BindLease(ref)
	}); err != nil {
		_ = stdin.Close()
		return err
	}
	started = true
	c.controller = controller
	c.guardRef = controller.Reference()
	s.mu.Lock()
	s.fingerprint = launch.Fingerprint
	s.child, s.last = c, c
	s.exitErr = nil
	if s.idleTimer == nil {
		s.idleTimer = time.AfterFunc(localIdleClose, s.idleClose)
	} else {
		s.idleTimer.Reset(localIdleClose)
	}
	s.mu.Unlock()
	if launch.OnStart != nil {
		launch.OnStart(cmd.Process.Pid)
	}
	go s.supervise(c)
	return nil
}

// supervise waits for one child, records its exit and drops the session
// entry when that child was still the current one.
func (s *localSession) supervise(c *child) {
	err := c.controller.Wait()
	c.stderrPipe.finish()
	exit := exitError(err, c.stderr.tail(true))
	s.mu.Lock()
	current := s.child == c
	if current {
		s.child = nil
		s.exitErr = exit
	}
	s.mu.Unlock()
	if current {
		s.stopIdleTimer()
		s.host.drop(s)
	}
	close(c.exited)
	s.wake()
}
