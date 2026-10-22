// Package processguard supervises a native process group without giving native
// processes ownership of runner locks. Importing this package installs a private
// self-executable helper entrypoint, including in Go test and desktop binaries.
package processguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"golang.org/x/sys/unix"
)

const privateArg = "--dyna-private-processguard-v1"

type identity struct {
	Socket   string `json:"socket"`
	Nonce    string `json:"nonce"`
	Guardian int    `json:"guardian"`
	Group    int    `json:"group"`
	Birth    string `json:"birth"`
}

// Reference is an immutable, JSON-safe capability. Copies refer to the same
// group. It contains no file descriptors and grants no authority through a PID
// alone. Keep it private to the resource record that owns the process.
type Reference struct{ value identity }

// Leader names the anchor for its whole life. A Start bind callback can
// record this ancestor before any native command runs. Birth is the platform's
// process start time, so a reused PID cannot inherit the launch record.
func (r Reference) Leader() (pid int, birth string) { return r.value.Group, r.value.Birth }

func (r Reference) MarshalJSON() ([]byte, error) { return json.Marshal(r.value) }
func (r *Reference) UnmarshalJSON(b []byte) error {
	var v identity
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.Socket == "" || len(v.Nonce) != 64 || v.Guardian <= 1 || v.Group <= 1 || v.Birth == "" {
		return errors.New("invalid processguard reference")
	}
	r.value = v
	return nil
}

type message struct {
	Op     string   `json:"op,omitempty"`
	Nonce  string   `json:"nonce,omitempty"`
	Role   string   `json:"role,omitempty"`
	Path   string   `json:"path,omitempty"`
	Args   []string `json:"args,omitempty"`
	Env    []string `json:"env"`
	Dir    string   `json:"dir,omitempty"`
	Ref    identity `json:"ref,omitempty"`
	Signal int      `json:"signal,omitempty"`
	Code   int      `json:"code,omitempty"`
	Error  string   `json:"error,omitempty"`
	Core   bool     `json:"core,omitempty"`
}

type wire struct {
	net.Conn
	enc *json.Encoder
	dec *json.Decoder
	mu  sync.Mutex
}

func newWire(c net.Conn) *wire {
	return &wire{Conn: c, enc: json.NewEncoder(c), dec: json.NewDecoder(c)}
}
func (w *wire) send(m message) error      { w.mu.Lock(); defer w.mu.Unlock(); return w.enc.Encode(m) }
func (w *wire) receive() (message, error) { var m message; err := w.dec.Decode(&m); return m, err }
func (w *wire) messages() <-chan message {
	c := make(chan message, 1)
	go func() {
		defer close(c)
		for {
			m, err := w.receive()
			if err != nil {
				return
			}
			c <- m
		}
	}()
	return c
}

// Controller owns one attempt. Configure Cmd's streams before Start. Call Wait
// exactly once after a successful Start. Wait includes native group cleanup.
type Controller struct {
	cmd           *exec.Cmd
	mu            sync.Mutex
	ref           Reference
	control       *wire
	started       bool
	nativeStarted bool
	cancelSignal  syscall.Signal
	done          chan struct{}
	waitErr       error
}

// New makes a command controller. Call Cmd to configure its byte streams.
func New(ctx context.Context, name string, args ...string) (*Controller, error) {
	return Wrap(exec.CommandContext(ctx, name, args...))
}
func (c *Controller) Cmd() *exec.Cmd { return c.cmd }

// Wrap keeps Cmd's stdin/stdout/stderr plumbing and context cancellation. It
// rejects ExtraFiles: native processes must never inherit resource lock files.
// Native SysProcAttr is owned by this package and must be unset or Setpgid only.
func Wrap(cmd *exec.Cmd) (*Controller, error) {
	if cmd == nil {
		return nil, errors.New("nil processguard command")
	}
	if cmd.Process != nil {
		return nil, errors.New("processguard command already started")
	}
	if len(cmd.ExtraFiles) != 0 {
		return nil, errors.New("processguard forbids native ExtraFiles")
	}
	if cmd.SysProcAttr != nil && !reflect.DeepEqual(cmd.SysProcAttr, &syscall.SysProcAttr{}) &&
		!reflect.DeepEqual(cmd.SysProcAttr, &syscall.SysProcAttr{Setpgid: true}) {
		return nil, errors.New("processguard requires exclusive process group attributes")
	}
	return &Controller{cmd: cmd, done: make(chan struct{})}, nil
}

func (c *Controller) Reference() Reference { c.mu.Lock(); defer c.mu.Unlock(); return c.ref }

// SetCancelSignal selects context cancellation before Start. SIGTERM is the
// default. SIGKILL is available for callers that require immediate retirement.
func (c *Controller) SetCancelSignal(sig syscall.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return errors.New("processguard already started")
	}
	if sig != syscall.SIGTERM && sig != syscall.SIGKILL {
		return errors.New("processguard cancellation requires SIGTERM or SIGKILL")
	}
	c.cancelSignal = sig
	return nil
}

// Start calls bind while the native bootstrap gate is closed. If bind fails,
// no native executable runs. Cleanup finishes before Start returns the error.
func (c *Controller) Start(bind func(Reference) error) (retErr error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("processguard already started")
	}
	c.started = true
	cancelSignal := c.cancelSignal
	if cancelSignal == 0 {
		cancelSignal = syscall.SIGTERM
	}
	c.mu.Unlock()
	defer func() {
		if retErr != nil {
			c.waitErr = retErr
			close(c.done)
		}
	}()
	cmd := c.cmd
	if len(cmd.ExtraFiles) != 0 {
		return errors.New("processguard forbids native ExtraFiles")
	}
	config := message{Role: "guard", Path: cmd.Path, Args: cmd.Args, Env: childenv.WithoutLocks(cmd.Environ()), Dir: cmd.Dir}
	parent, file, err := socketPair()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		parent.Close()
		file.Close()
		return err
	}
	nonce := randomNonce()
	cmd.Path, cmd.Args, cmd.Env = exe, []string{exe, privateArg, nonce}, childenv.WithoutLocks(nil)
	cmd.Dir = "" // Resolve the native Dir exactly once, in the anchor.
	cmd.ExtraFiles = []*os.File{file}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			c.mu.Lock()
			running := c.nativeStarted
			c.mu.Unlock()
			if !running {
				return parent.Close()
			}
			return c.Signal(cancelSignal)
		}
	}
	c.control = parent
	if err := cmd.Start(); err != nil {
		parent.Close()
		file.Close()
		return err
	}
	file.Close()
	config.Nonce = nonce
	if err = parent.send(config); err == nil {
		var ready message
		ready, err = parent.receive()
		if err == nil && ready.Error != "" {
			err = errors.New(ready.Error)
		}
		if err == nil && ready.Op != "ready" {
			err = errors.New("invalid processguard handshake")
		}
		if err == nil {
			c.mu.Lock()
			c.ref = Reference{ready.Ref}
			c.mu.Unlock()
		}
	}
	if err == nil && bind != nil {
		err = bind(c.Reference())
	}
	if err == nil {
		err = parent.send(message{Op: "start"})
	}
	if err == nil {
		var started message
		started, err = parent.receive()
		if err == nil && started.Error != "" {
			err = errors.New(started.Error)
		}
		if err == nil && started.Op != "started" {
			err = errors.New("invalid native start reply")
		}
	}
	if err != nil {
		parent.Close()
		_ = cmd.Wait()
		c.cleanupFallback()
		return err
	}
	c.mu.Lock()
	c.nativeStarted = true
	c.mu.Unlock()
	// Keep this reader active even before Wait. A dead guard must never leave
	// native work running while the caller processes output or resource records.
	go func() {
		for {
			if _, err := parent.receive(); err != nil {
				break
			}
		}
		c.cleanupFallback()
		c.waitErr = cmd.Wait()
		parent.Close()
		close(c.done)
	}()
	return nil
}

func (c *Controller) cleanupFallback() {
	r := c.Reference().value
	if r.Group <= 1 {
		return
	}
	// The anchor survives until group SIGKILL, and its control-pipe EOF also
	// kills the group if the guard dies. Never send a signal to a reused PID.
	if birth, err := processBirth(r.Group); err == nil && birth == r.Birth {
		_ = syscall.Kill(-r.Group, syscall.SIGKILL)
	} else if err == nil {
		return // A new group leader has reused this PID.
	}
	for {
		live, err := referenceLive(r)
		if err == nil && !live {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (c *Controller) Wait() error                     { <-c.done; return c.waitErr }
func (c *Controller) Signal(sig syscall.Signal) error { return Signal(c.Reference(), sig) }

// Probe reports liveness until all non-zombie group members have stopped.
// Inspection failures return true and an error; callers must preserve protection.
func Probe(ref Reference) (bool, error) {
	r := ref.value
	if r.Group <= 1 {
		return false, errors.New("invalid processguard reference")
	}
	if _, err := request(r, message{Op: "probe"}); err == nil {
		return true, nil
	}
	return referenceLive(r)
}

func referenceLive(r identity) (bool, error) {
	if birth, err := processBirth(r.Group); err == nil && birth != r.Birth {
		return false, nil
	}
	return groupLive(r.Group)
}

// ErrGroupEnded reports that the guarded process group has already exited,
// whether or not its guard is still running.
var ErrGroupEnded = errors.New("native group has ended")

// Signal authenticates the private guard socket. A bare PID never grants
// authority. Guard failure triggers automatic cleanup through both control pipes.
func Signal(ref Reference, sig syscall.Signal) error {
	r := ref.value
	if r.Group <= 1 {
		return errors.New("invalid processguard reference")
	}
	_, err := request(r, message{Op: "signal", Signal: int(sig)})
	if err != nil && !errors.Is(err, ErrGroupEnded) {
		// The guard exits once the group has ended, so its socket can be gone.
		if live, probeErr := referenceLive(r); probeErr == nil && !live {
			return ErrGroupEnded
		}
	}
	return err
}

func request(r identity, m message) (message, error) {
	conn, err := net.DialTimeout("unix", r.Socket, time.Second)
	if err != nil {
		return message{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	w := newWire(conn)
	m.Nonce = r.Nonce
	if err := w.send(m); err != nil {
		return message{}, err
	}
	answer, err := w.receive()
	if err == nil && answer.Error == ErrGroupEnded.Error() {
		err = ErrGroupEnded
	} else if err == nil && answer.Error != "" {
		err = errors.New(answer.Error)
	}
	return answer, err
}

func randomNonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func socketPair() (*wire, *os.File, error) {
	// Coordinate with os/exec on platforms without atomic SOCK_CLOEXEC.
	syscall.ForkLock.RLock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		syscall.ForkLock.RUnlock()
		return nil, nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	syscall.ForkLock.RUnlock()
	a, b := os.NewFile(uintptr(fds[0]), "guard-parent"), os.NewFile(uintptr(fds[1]), "guard-child")
	conn, err := net.FileConn(a)
	a.Close()
	if err != nil {
		b.Close()
		return nil, nil, err
	}
	return newWire(conn), b, nil
}

func drainGroup(group int) {
	for {
		live, err := groupLive(group)
		if err == nil && !live {
			return
		}
		// Do not time out: uncertainty must not release resource protection.
		time.Sleep(5 * time.Millisecond)
	}
}

func exitStatus(m message) {
	if m.Signal != 0 {
		// Go's default SIGQUIT/SIGABRT handlers print runtime diagnostics and
		// can exit with status 2. Restore the kernel default disposition instead.
		if err := prepareSignalReplay(syscall.Signal(m.Signal)); err != nil {
			os.Exit(125)
		}
		_ = syscall.Kill(os.Getpid(), syscall.Signal(m.Signal))
		select {}
	}
	os.Exit(m.Code)
}

func prepareSignalReplay(sig syscall.Signal) error {
	// Reproduce the signal identity, never a second core dump. Linux pipe
	// handlers can ignore RLIMIT_CORE, so also disable process dumpability.
	if err := disableCoreDumps(); err != nil {
		return err
	}
	return defaultSignal(sig)
}

func status(err error) message {
	if err == nil {
		return message{Op: "exit"}
	}
	var e *exec.ExitError
	if errors.As(err, &e) {
		s := e.Sys().(syscall.WaitStatus)
		if s.Signaled() {
			return message{Op: "exit", Signal: int(s.Signal()), Core: s.CoreDump()}
		}
		return message{Op: "exit", Code: s.ExitStatus()}
	}
	return message{Op: "exit", Code: 127, Error: fmt.Sprint(err)}
}
