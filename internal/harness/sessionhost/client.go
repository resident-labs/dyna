package sessionhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/processguard"
)

const (
	// handshakeWait bounds every wait for the host's next message. A host
	// that stays silent longer is killed by its peer pid, and the next
	// attach starts a fresh one.
	handshakeWait = 30 * time.Second
	// detachWait bounds the clean detach acknowledgement.
	detachWait = 5 * time.Second
	// stopWait bounds Close and Halt: the host's orderly stop plus kill.
	stopWait = localStopGrace + 5*time.Second
)

// errHostGone marks a host connection that ended before it answered, so the
// attach can retry once against a fresh host.
var errHostGone = errors.New("session host connection lost")

// socketHost is the Host that talks to detached host processes. It keeps no
// registry beyond the keys this process attached, for ResetForTest.
type socketHost struct {
	mu      sync.Mutex
	dir     string
	used    map[string]bool
	pending map[string]chan struct{} // id -> closed when a detach is acknowledged
}

func newSocketHost(dir string) *socketHost {
	return &socketHost{dir: dir, used: map[string]bool{}, pending: map[string]chan struct{}{}}
}

// privateDir returns the socket directory, creating it 0700 and refusing one
// that another user owns or others can enter.
func (h *socketHost) privateDir() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" {
		h.dir = defaultDir()
	}
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(h.dir)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("session host directory %s must be a private directory", h.dir)
	}
	return h.dir, nil
}

// defaultDir keeps socket paths under the 104-byte Unix limit. A test binary
// gets its own directory, so concurrent test binaries never share a host.
func defaultDir() string {
	if testing.Testing() {
		// Tests often point TMPDIR at a long per-test folder; stay short.
		if dir, err := os.MkdirTemp(shortTempDir(), "dh"); err == nil {
			return dir
		}
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" || len(base) > 60 {
		base = shortTempDir()
	}
	return filepath.Join(base, fmt.Sprintf("dyna-hosts-%d", os.Getuid()))
}

func shortTempDir() string {
	if dir := os.TempDir(); len(dir) <= 60 {
		return dir
	}
	return "/tmp"
}

// Session attaches the key's host, spawning a detached one when none is
// listening. A host that exits as the client connects is retried once.
func (h *socketHost) Session(ctx context.Context, launch Launch) (Session, error) {
	if launch.Key == "" || len(launch.Argv) == 0 {
		return nil, errors.New("session launch needs a key and a command line")
	}
	id := launch.id()
	h.waitDetached(ctx, id)
	for attempt := 0; ; attempt++ {
		conn, err := h.connect(ctx, id)
		if err != nil {
			return nil, err
		}
		r := newRemote(conn)
		r.host, r.id = h, id
		reply, err := r.call(ctx, message{Op: opAttach, Launch: toWire(launch)}, handshakeWait, launch.BindLease)
		if err == nil {
			h.mu.Lock()
			h.used[id] = true
			h.mu.Unlock()
			if reply.Started && launch.OnStart != nil {
				launch.OnStart(reply.Pid)
			}
			return r, nil
		}
		_ = conn.Close()
		if attempt == 0 && errors.Is(err, errHostGone) {
			continue
		}
		return nil, err
	}
}

// waitDetached waits for this process's own pending detach of id to be
// acknowledged, so a turn that follows a detach never sees its own key held.
// The wait is bounded by the detach acknowledgement bound.
func (h *socketHost) waitDetached(ctx context.Context, id string) {
	h.mu.Lock()
	done := h.pending[id]
	h.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// connect dials the key's host, replacing a stale socket with a fresh host
// under the key's lock so two clients never spawn two hosts.
func (h *socketHost) connect(ctx context.Context, id string) (net.Conn, error) {
	dir, err := h.privateDir()
	if err != nil {
		return nil, err
	}
	socket, lock := hostPaths(dir, id)
	release, err := lockFile(lock)
	if err != nil {
		return nil, err
	}
	defer release()
	conn, err := net.DialTimeout("unix", socket, detachWait)
	if err == nil {
		return conn, nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
		return nil, fmt.Errorf("dial session host: %w", err)
	}
	_ = os.Remove(socket)
	if err := spawnHost(ctx, socket); err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", socket, detachWait)
}

// spawnHost starts a detached host on socket and waits until it listens.
func spawnHost(ctx context.Context, socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ready, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer ready.Close()
	cmd := exec.Command(exe, privateArg, socket)
	cmd.Env = childenv.WithoutLocks(os.Environ())
	cmd.ExtraFiles = []*os.File{writer}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = writer.Close()
	if err != nil {
		return fmt.Errorf("start session host: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	wait, cancel := context.WithTimeout(ctx, handshakeWait)
	defer cancel()
	deadline, _ := wait.Deadline()
	_ = ready.SetReadDeadline(deadline)
	if n, err := ready.Read(make([]byte, 1)); n != 1 {
		_ = cmd.Process.Kill()
		return fmt.Errorf("session host did not start: %v", err)
	}
	return nil
}

// shutdownUsed asks the host of every key this process attached to stop its
// child and exit. It never spawns a host.
func (h *socketHost) shutdownUsed() {
	h.mu.Lock()
	dir := h.dir
	keys := make([]string, 0, len(h.used))
	for key := range h.used {
		keys = append(keys, key)
	}
	h.used = map[string]bool{}
	h.mu.Unlock()
	for _, key := range keys {
		socket, _ := hostPaths(dir, key)
		conn, err := net.DialTimeout("unix", socket, detachWait)
		if err != nil {
			continue
		}
		r := newRemote(conn)
		_, _ = r.call(context.Background(), message{Op: opShutdown}, stopWait, nil)
		_ = conn.Close()
	}
}

// remoteSession is one attached client connection.
type remoteSession struct {
	conn    net.Conn
	host    *socketHost
	id      string
	hostPid int
	wmu     sync.Mutex
	enc     *json.Encoder
	cmu     sync.Mutex // one control call at a time
	replies chan message
	gone    chan struct{}
	frames  *frameQueue
	closed  sync.Once
	// tail is the stderr the host reported at Close.
	tail       string
	closedTail bool
}

func newRemote(conn net.Conn) *remoteSession {
	r := &remoteSession{conn: conn, hostPid: peerPID(conn), enc: json.NewEncoder(conn),
		replies: make(chan message, 4), gone: make(chan struct{}), frames: newFrameQueue()}
	go r.read()
	return r
}

// read routes the host's pushed frames to the queue and its replies to the
// caller of the current control op.
func (r *remoteSession) read() {
	dec := json.NewDecoder(r.conn)
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			r.frames.fail(fmt.Errorf("%w: %v", errHostGone, err))
			close(r.gone)
			return
		}
		switch m.Op {
		case opFrame:
			r.frames.push(m.Data)
		case opExit:
			r.frames.fail(errors.New(m.Error))
		default:
			if m.Fresh {
				r.frames.reset()
			}
			r.replies <- m
		}
	}
}

func (r *remoteSession) send(m message) error {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	return r.enc.Encode(m)
}

// call sends one control op and returns the host's reply. While the op is
// open the host may ask the client to bind its leases; bind answers it.
// Every wait for the host is bounded by wait; a silent host is killed.
func (r *remoteSession) call(ctx context.Context, m message, wait time.Duration, bind func(processguard.Reference) error) (message, error) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	if err := r.send(m); err != nil {
		return message{}, fmt.Errorf("%w: %v", errHostGone, err)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var bindErr error
	for {
		select {
		case reply := <-r.replies:
			if reply.Op == opBind {
				ack := message{Op: opBound}
				if bind != nil && reply.Ref != nil {
					if bindErr = bind(*reply.Ref); bindErr != nil {
						ack.Error = bindErr.Error()
					}
				}
				if err := r.send(ack); err != nil {
					return message{}, fmt.Errorf("%w: %v", errHostGone, err)
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
				continue
			}
			if reply.Error != "" {
				if bindErr != nil {
					return reply, bindErr
				}
				return reply, errors.New(reply.Error)
			}
			return reply, nil
		case <-r.gone:
			return message{}, errHostGone
		case <-timer.C:
			r.kill()
			return message{}, fmt.Errorf("session host did not answer %s within %s", m.Op, wait)
		case <-ctx.Done():
			_ = r.conn.Close()
			return message{}, ctx.Err()
		}
	}
}

// kill ends a host that stopped answering. Its processguard guard sees the
// host die and kills the child's group.
func (r *remoteSession) kill() {
	if r.hostPid > 1 && r.hostPid != os.Getpid() {
		_ = syscall.Kill(r.hostPid, syscall.SIGKILL)
	}
	_ = r.conn.Close()
}

func (r *remoteSession) Write(frame []byte) error {
	_, err := r.call(context.Background(), message{Op: opWrite, Data: frame}, handshakeWait, nil)
	return err
}

func (r *remoteSession) Next(ctx context.Context) ([]byte, error) { return r.frames.pop(ctx) }

func (r *remoteSession) Replace(ctx context.Context, launch Launch) error {
	reply, err := r.call(ctx, message{Op: opReplace, Launch: toWire(launch)}, handshakeWait, launch.BindLease)
	if err == nil && reply.Started && launch.OnStart != nil {
		launch.OnStart(reply.Pid)
	}
	return err
}

// Detach ends the attachment without blocking: the request goes out and its
// acknowledgement is awaited in the background, bounded by detachWait. The
// next Session on the same host waits for that acknowledgement, so it never
// races the release. A host that does not answer sees the closed connection,
// which it treats as an abandoned attachment.
func (r *remoteSession) Detach(reason DetachReason) {
	done := make(chan struct{})
	r.host.mu.Lock()
	r.host.pending[r.id] = done
	r.host.mu.Unlock()
	r.frames.fail(errors.New("session detached"))
	go func() {
		_, _ = r.call(context.Background(), message{Op: opDetach, Reason: reason}, detachWait, nil)
		r.finish("session detached")
		r.host.mu.Lock()
		if r.host.pending[r.id] == done {
			delete(r.host.pending, r.id)
		}
		r.host.mu.Unlock()
		close(done)
	}()
}

// Close stops the child and waits until the host has drained every frame it
// wrote. Frames and the stderr tail stay readable afterwards.
func (r *remoteSession) Close() error {
	reply, err := r.call(context.Background(), message{Op: opClose}, stopWait, nil)
	r.cmu.Lock()
	r.tail, r.closedTail = string(reply.Data), true
	r.cmu.Unlock()
	r.finish("session closed")
	return err
}

func (r *remoteSession) Halt() {
	_, _ = r.call(context.Background(), message{Op: opHalt}, stopWait, nil)
}

func (r *remoteSession) KillGroup() {
	_, _ = r.call(context.Background(), message{Op: opKill}, detachWait, nil)
}

func (r *remoteSession) StderrTail() string {
	r.cmu.Lock()
	closed, tail := r.closedTail, r.tail
	r.cmu.Unlock()
	if closed {
		return tail
	}
	reply, err := r.call(context.Background(), message{Op: opStderr}, detachWait, nil)
	if err != nil {
		return ""
	}
	return string(reply.Data)
}

// finish closes the connection once and fails later reads.
func (r *remoteSession) finish(reason string) {
	r.closed.Do(func() {
		r.frames.fail(errors.New(reason))
		_ = r.conn.Close()
	})
}

// frameQueue holds the frames the host pushed until the codec asks for them.
// Frames queued before a failure are returned first.
type frameQueue struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
	wake   chan struct{}
}

func newFrameQueue() *frameQueue { return &frameQueue{wake: make(chan struct{}, 1)} }

func (q *frameQueue) push(frame []byte) {
	q.mu.Lock()
	q.frames = append(q.frames, frame)
	q.mu.Unlock()
	q.signal()
}

// fail records the first error; later frames still drain before it shows.
func (q *frameQueue) fail(err error) {
	q.mu.Lock()
	if q.err == nil {
		q.err = err
	}
	q.mu.Unlock()
	q.signal()
}

// reset forgets the frames and failure of a child that was replaced.
func (q *frameQueue) reset() {
	q.mu.Lock()
	q.frames, q.err = nil, nil
	q.mu.Unlock()
}

func (q *frameQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *frameQueue) pop(ctx context.Context) ([]byte, error) {
	for {
		q.mu.Lock()
		if len(q.frames) > 0 {
			frame := q.frames[0]
			q.frames = q.frames[1:]
			q.mu.Unlock()
			return frame, nil
		}
		err := q.err
		q.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-q.wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
