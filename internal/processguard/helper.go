package processguard

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
)

// This entrypoint requires an exact private argument vector, a socket on fd 3,
// and a matching 256-bit nonce in the first frame. An environment variable alone
// cannot activate it. Running in init keeps it independent of either main and
// of the Go testing flag parser. Helpers never interpret normal CLI arguments.
func init() {
	if len(os.Args) != 3 || os.Args[1] != privateArg || len(os.Args[2]) != 64 {
		return
	}
	file := os.NewFile(3, "processguard-control")
	if file == nil {
		os.Exit(126)
	}
	syscall.CloseOnExec(3)
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		os.Exit(126)
	}
	w := newWire(conn)
	_ = w.SetReadDeadline(time.Now().Add(10 * time.Second))
	m, err := w.receive()
	_ = w.SetReadDeadline(time.Time{})
	if err != nil || m.Nonce != os.Args[2] {
		os.Exit(126)
	}
	switch m.Role {
	case "guard":
		runGuard(w, m)
	case "anchor":
		runAnchor(w, m)
	default:
		os.Exit(126)
	}
	os.Exit(126)
}

// socketParent keeps the control socket path under the 104-byte Unix limit.
// A long TMPDIR falls back to /tmp.
func socketParent() string {
	dir := os.TempDir()
	if len(dir)+len("/dyna-pg-0000000000/control.sock") >= 104 {
		return "/tmp"
	}
	return dir
}

func runGuard(parent *wire, config message) {
	children := make(chan os.Signal, 1)
	signal.Notify(children, syscall.SIGCHLD)
	defer signal.Stop(children)
	if err := becomeSubreaper(); err != nil {
		_ = parent.send(message{Error: err.Error()})
		os.Exit(126)
	}
	root, err := os.MkdirTemp(socketParent(), "dyna-pg-")
	if err != nil {
		_ = parent.send(message{Error: err.Error()})
		os.Exit(126)
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = parent.send(message{Error: err.Error()})
		os.Exit(126)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		_ = parent.send(message{Error: err.Error()})
		return
	}
	anchor, file, err := socketPair()
	if err != nil {
		_ = parent.send(message{Error: err.Error()})
		return
	}
	exe, err := fsutil.RunningExecutable()
	if err != nil {
		file.Close()
		anchor.Close()
		_ = parent.send(message{Error: err.Error()})
		return
	}
	key := randomNonce()
	cmd := exec.Command(exe, privateArg, key)
	cmd.Env = childenv.WithoutLocks(nil)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{file}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		file.Close()
		anchor.Close()
		_ = parent.send(message{Error: err.Error()})
		return
	}
	file.Close()
	group := cmd.Process.Pid
	// The anchor's lifetime pins the process group ID. Always kill and drain
	// before closing the liveness socket, including failure before the bind.
	defer func() {
		_ = syscall.Kill(-group, syscall.SIGKILL)
		_ = cmd.Wait()
		drainGroup(group)
		reapGroup(group)
		reapOrphans(0)
		anchor.Close()
	}()
	config.Role, config.Nonce = "anchor", key
	if err := anchor.send(config); err != nil {
		_ = parent.send(message{Error: err.Error()})
		return
	}
	ready, err := anchor.receive()
	if err != nil || ready.Op != "ready" {
		_ = parent.send(message{Error: "anchor bootstrap failed"})
		return
	}
	birth, err := processBirth(group)
	if err != nil {
		_ = parent.send(message{Error: err.Error()})
		return
	}
	ref := identity{Socket: path, Nonce: randomNonce(), Guardian: os.Getpid(), Group: group, Birth: birth}
	var groupSignal atomic.Int32
	go serve(listener, ref, &groupSignal)
	if err := parent.send(message{Op: "ready", Ref: ref}); err != nil {
		return
	}
	parents := parent.messages()
	m, ok := <-parents
	if !ok || m.Op != "start" {
		return
	}
	if err := anchor.send(message{Op: "start"}); err != nil {
		_ = parent.send(message{Error: err.Error()})
		return
	}
	m, err = anchor.receive()
	if err != nil {
		_ = parent.send(message{Error: "anchor start failed"})
		return
	}
	if err := parent.send(m); err != nil || m.Error != "" {
		return
	}
	anchors := anchor.messages()
	result := message{Code: 125}
	running := true
	for running {
		select {
		case <-children:
			// The anchor owns the native command's Wait and reports its status
			// over the socket. Keep the anchor waitable, even after it exits,
			// to pin the group ID until cleanup sends the group kill below.
			// Publish each live reap, so the runner can account for adopted
			// children instead of reading the process table.
			if reaped := reapOrphans(group); reaped > 0 {
				_ = parent.send(message{Op: "reaped", Code: reaped})
			}
		case <-parents: // EOF is the runner's crash signal.
			running = false
		case m, ok := <-anchors:
			if ok && m.Op == "exit" {
				result = m
			} else if groupSignal.Load() == int32(syscall.SIGKILL) {
				result = message{Signal: int(syscall.SIGKILL)}
			}
			running = false
		}
	}
	// Explicit cleanup here lets status replication happen after every owned
	// group member has stopped. The socket remains live throughout the drain.
	_ = syscall.Kill(-group, syscall.SIGKILL)
	_ = cmd.Wait()
	drainGroup(group)
	reapGroup(group)
	reapOrphans(0)
	anchor.Close()
	listener.Close()
	os.RemoveAll(root)
	parent.Close()
	exitStatus(result)
}

func serve(listener net.Listener, ref identity, groupSignal *atomic.Int32) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			w := newWire(conn)
			m, err := w.receive()
			if err != nil || m.Nonce != ref.Nonce {
				return
			}
			answer := message{Op: "ok"}
			switch m.Op {
			case "probe":
			case "signal":
				// Group identity is pinned by the anchor until cleanup.
				birth, err := processBirth(ref.Group)
				if err != nil || birth != ref.Birth {
					answer.Error = ErrGroupEnded.Error()
				} else {
					groupSignal.Store(int32(m.Signal))
					if err := syscall.Kill(-ref.Group, syscall.Signal(m.Signal)); err != nil {
						answer.Error = err.Error()
					}
				}
			default:
				answer.Error = "invalid processguard operation"
			}
			_ = w.send(answer)
		}()
	}
}

func runAnchor(control *wire, config message) {
	// Catch group termination signals in the anchor, so it can report the
	// native wait status. Caught dispositions reset at native exec. Do not use
	// signal.Ignore: ignored dispositions would reach the native executable.
	ignored := make(chan os.Signal, 32)
	signal.Notify(ignored)
	// A guard crash closes this socket. Native exec inherits no copy, so even
	// a background grandchild cannot keep the crash channel open.
	die := func() { _ = syscall.Kill(-os.Getpid(), syscall.SIGKILL); os.Exit(126) }
	if err := control.send(message{Op: "ready"}); err != nil {
		die()
	}
	commands := control.messages()
	m, ok := <-commands
	if !ok || m.Op != "start" {
		die()
	}
	cmd := &exec.Cmd{Path: config.Path, Args: config.Args, Env: config.Env, Dir: config.Dir,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if err := childenv.Clean(cmd).Start(); err != nil {
		_ = control.send(message{Op: "started", Error: err.Error()})
		<-commands
		die()
	}
	if err := control.send(message{Op: "started"}); err != nil {
		die()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-commands:
		die()
	case err := <-done:
		if err := control.send(status(err)); err != nil {
			die()
		}
	}
	// Keep the group leader alive after native exit. The guard kills and
	// drains background children before it removes liveness protection.
	<-commands
	die()
}
