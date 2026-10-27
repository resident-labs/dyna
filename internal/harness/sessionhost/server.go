package sessionhost

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/processguard"
)

// firstAttachGrace is how long a fresh host waits for its first client
// before it exits.
const firstAttachGrace = 30 * time.Second

// The host entrypoint runs in init, like the processguard helper, so every
// Go binary that imports this package (dyna, the desktop app, test binaries)
// can act as a host. Only the exact private argument vector activates it.
func init() {
	if len(os.Args) == 3 && os.Args[1] == privateArg {
		runHost(os.Args[2])
	}
}

// runHost serves one socket until the host has no child and no client, or a
// signal ends it. The ready pipe on fd 3 tells the spawner the socket is
// listening.
func runHost(socket string) {
	ready := os.NewFile(3, "sessionhost-ready")
	ln, err := net.Listen("unix", socket)
	if err == nil {
		err = os.Chmod(socket, 0o600)
	}
	if err != nil {
		os.Exit(1)
	}
	// Clients remove a stale socket under their lock; the host never does, so
	// it cannot unlink a newer host's socket.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	sv := newServer(ln)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-signals
		sv.shutdown()
	}()
	checks := time.NewTicker(socketCheckEvery)
	defer checks.Stop()
	go watchSocket(checks.C, socket, sv.shutdown, sv.done)
	_, _ = ready.Write([]byte{1})
	_ = ready.Close()
	sv.run()
	os.Exit(0)
}

// socketCheckEvery is how often a host checks that its socket still exists.
const socketCheckEvery = time.Minute

// socketGone reports whether the socket file no longer exists.
func socketGone(socket string) bool {
	_, err := os.Lstat(socket)
	return errors.Is(err, fs.ErrNotExist)
}

// watchSocket ends a host whose socket file is gone. No client can reach such a
// host, so it and its children would run until the machine restarts: a test
// that was killed before it could stop its host removes its folder later, and
// the host would stay. The check ends when done closes.
func watchSocket(ticks <-chan time.Time, socket string, stop func(), done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case _, open := <-ticks:
			if !open {
				return
			}
			if socketGone(socket) {
				stop()
				return
			}
		}
	}
}

// server is one host process: one listener and the localHost that owns its
// children.
type server struct {
	host *localHost
	ln   net.Listener

	mu      sync.Mutex
	conns   int
	stopped bool
	done    chan struct{}
}

func newServer(ln net.Listener) *server {
	sv := &server{host: newLocalHost(), ln: ln, done: make(chan struct{})}
	sv.host.onEmpty = sv.maybeExit
	return sv
}

// run accepts clients until the server stops.
func (sv *server) run() {
	grace := time.AfterFunc(firstAttachGrace, sv.maybeExit)
	defer grace.Stop()
	go sv.accept()
	<-sv.done
}

func (sv *server) accept() {
	for {
		conn, err := sv.ln.Accept()
		if err != nil {
			return
		}
		sv.mu.Lock()
		if sv.stopped {
			sv.mu.Unlock()
			_ = conn.Close()
			return
		}
		sv.conns++
		sv.mu.Unlock()
		go sv.handle(conn)
	}
}

// maybeExit stops the server when no client is connected and no child is
// left. A client that dialed in the same instant sees its connection reset
// and retries with a fresh host.
func (sv *server) maybeExit() {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.conns == 0 && sv.host.count() == 0 {
		sv.stopLocked()
	}
}

func (sv *server) stopLocked() {
	if sv.stopped {
		return
	}
	sv.stopped = true
	_ = sv.ln.Close()
	close(sv.done)
}

// shutdown stops every child, orderly then killed, and ends the server.
func (sv *server) shutdown() {
	sv.shutdownWith(false)
}

// shutdownWith stops every child. When fireStopGrace is set, the orderly stop
// grace is an already-closed channel, so a child that ignores closed stdin is
// killed at once. Production calls shutdown and still waits the grace.
func (sv *server) shutdownWith(fireStopGrace bool) {
	if fireStopGrace {
		fired := make(chan time.Time)
		close(fired)
		stopGraceAfter = func(time.Duration) <-chan time.Time { return fired }
	}
	sv.host.closeAll()
	sv.mu.Lock()
	sv.stopLocked()
	sv.mu.Unlock()
}

func (sv *server) handle(conn net.Conn) {
	c := &hostConn{sv: sv, conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	c.loop()
	_ = conn.Close()
	sv.mu.Lock()
	sv.conns--
	sv.mu.Unlock()
	sv.maybeExit()
}

// hostConn is one attached client.
type hostConn struct {
	sv   *server
	conn net.Conn
	dec  *json.Decoder

	wmu sync.Mutex
	enc *json.Encoder

	sess   *localSession
	cancel context.CancelFunc
	pumped chan struct{}
}

func (c *hostConn) send(m message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.enc.Encode(m)
}

func (c *hostConn) reply(err error, m message) {
	m.Op = opOK
	if err != nil {
		m.Error = err.Error()
	}
	_ = c.send(m)
}

// loop serves control ops until the client goes away. A client that
// disappears while attached abandoned its run: the child's group is killed.
func (c *hostConn) loop() {
	defer func() {
		if c.sess != nil {
			c.stopPump()
			c.sess.Detach(DetachAbandoned)
		}
	}()
	for {
		var m message
		if err := c.dec.Decode(&m); err != nil {
			return
		}
		if m.Op == opAttach || m.Op == opReplace {
			c.start(m)
			continue
		}
		if m.Op == opCloseKey {
			c.reply(c.sv.host.closeKey(m.Key), message{})
			continue
		}
		if m.Op == opIdleList {
			c.reply(nil, message{Idle: c.sv.host.idleSessions()})
			continue
		}
		if m.Op == opUnload {
			c.reply(nil, message{Done: c.sv.host.unloadIdle(m.Key, m.MinIdle)})
			continue
		}
		if m.Op == opShutdown {
			c.sv.shutdownWith(m.FireStopGrace)
			c.reply(nil, message{})
			return
		}
		if c.sess == nil {
			c.reply(errors.New("no attached session"), message{})
			continue
		}
		switch m.Op {
		case opWrite:
			c.reply(c.sess.Write(m.Data), message{})
		case opHalt:
			c.sess.Halt()
			c.reply(nil, message{})
		case opKill:
			c.sess.KillGroup()
			c.reply(nil, message{})
		case opStderr:
			c.reply(nil, message{Data: []byte(c.sess.StderrTail())})
		case opDetach:
			c.stopPump()
			c.sess.Detach(m.Reason)
			c.sess = nil
			c.reply(nil, message{})
		case opClose:
			// Close first, then let the pump drain: it forwards every frame the
			// child wrote before it exited, then ends on the exit error.
			err := c.sess.Close()
			c.stopPump(true)
			tail := c.sess.StderrTail()
			c.sess = nil
			c.reply(err, message{Data: []byte(tail)})
		default:
			c.reply(errors.New("unknown session op "+m.Op), message{})
		}
	}
}

// start attaches or replaces: the host asks the client to bind its leases to
// the child's reference, then answers whether it spawned a child.
func (c *hostConn) start(m message) {
	if m.Launch == nil {
		c.reply(errors.New("launch missing"), message{})
		return
	}
	var started bool
	var pid int
	launch := m.Launch.launch()
	launch.BindLease = c.bind
	launch.OnStart = func(p int) { started, pid = true, p }
	if m.Op == opReplace {
		if c.sess == nil {
			c.reply(errors.New("no attached session"), message{})
			return
		}
		c.stopPump()
		if err := c.sess.Replace(context.Background(), launch); err != nil {
			c.reply(err, message{})
			c.startPump()
			return
		}
	} else {
		if c.sess != nil {
			c.reply(errors.New("connection already attached"), message{})
			return
		}
		s, err := c.sv.host.Session(context.Background(), launch)
		if err != nil {
			c.reply(err, message{})
			return
		}
		c.sess = s.(*localSession)
	}
	c.reply(nil, message{Connection: c.sess.Connection(), Started: started, Pid: pid, Fresh: m.Op == opReplace})
	c.startPump()
}

// bind asks the client to bind its turn leases to the child's reference and
// waits for the answer. The child has not run yet when this is a spawn.
func (c *hostConn) bind(ref processguard.Reference) error {
	if err := c.send(message{Op: opBind, Ref: &ref}); err != nil {
		return err
	}
	var ack message
	if err := c.dec.Decode(&ack); err != nil {
		return err
	}
	if ack.Op != opBound {
		return errors.New("unexpected reply to bind")
	}
	if ack.Error != "" {
		return errors.New(ack.Error)
	}
	return nil
}

// startPump forwards the child's frames and its exit to the client.
func (c *hostConn) startPump() {
	ctx, cancel := context.WithCancel(context.Background())
	pumped := make(chan struct{})
	c.cancel, c.pumped = cancel, pumped
	go func() {
		defer close(pumped)
		for {
			frame, err := c.sess.Next(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				_ = c.send(message{Op: opExit, Error: err.Error()})
				return
			}
			if c.send(message{Op: opFrame, Data: frame}) != nil {
				return
			}
		}
	}()
}

// stopPump ends the frame pump. With drain it first waits for the pump to
// finish on its own, which it does once the child has exited.
func (c *hostConn) stopPump(drain ...bool) {
	if c.cancel == nil {
		return
	}
	if len(drain) > 0 && drain[0] {
		<-c.pumped
	}
	c.cancel()
	<-c.pumped
	c.cancel = nil
}
