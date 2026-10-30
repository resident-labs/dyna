// Package terminalsvc owns interactive host shells. A client connection never
// owns the process: output stays in a bounded buffer until close or idle reap.
package terminalsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

const ScrollbackBytes = 256 << 10
const MaxInputBytes = 64 << 10

var ErrNotFound = errors.New("no such terminal")

type Options struct {
	// Rooms and SessionRoot select room directories and shell job stores.
	Rooms       *room.Store
	SessionRoot string
	// SessionDirectory reads a session directory without importing its runtime.
	SessionDirectory func(ref string) (roomID, sessionID, cwd string, err error)
	// Directory resolves a room or session reference before TermOpen starts a shell.
	Directory    func(ref string) (roomID, sessionID, cwd string, err error)
	IdleTimeout  time.Duration // zero: one hour; negative: disabled
	Shell        string        // empty: $SHELL, then /bin/sh
	MaxTerminals int           // zero: 64
}

type terminal struct {
	mu     sync.Mutex
	input  sync.Mutex
	info   Info
	cmd    *exec.Cmd
	file   *os.File
	buffer []byte
	offset uint64
	wake   chan struct{}
	done   chan struct{}
	closed bool
}

type Service struct {
	mu       sync.Mutex
	options  Options
	terms    map[string]*terminal
	closed   bool
	stop     chan struct{}
	reapDone chan struct{}
}

func New(o Options) *Service {
	if o.IdleTimeout == 0 {
		o.IdleTimeout = time.Hour
	}
	if o.MaxTerminals <= 0 {
		o.MaxTerminals = 64
	}
	s := &Service{options: o, terms: make(map[string]*terminal), stop: make(chan struct{}), reapDone: make(chan struct{})}
	if o.Directory == nil && o.Rooms != nil && o.SessionDirectory != nil {
		s.options.Directory = s.TerminalDirectory
	}
	go s.reaper()
	return s
}

func dimensions(cols, rows uint16) error {
	if cols == 0 || rows == 0 || cols > 1000 || rows > 1000 {
		return errors.New("terminal size must be between 1 and 1000 columns and rows")
	}
	return nil
}

// Open starts the user's interactive login shell in the supplied directory.
func (s *Service) Open(roomID, sessionID, cwd string, cols, rows uint16) (Info, error) {
	return s.openShell(roomID, sessionID, cwd, nil, cols, rows)
}

// openShell starts the login shell with extra arguments, such as -c and a
// command line.
func (s *Service) openShell(roomID, sessionID, cwd string, extra []string, cols, rows uint16) (Info, error) {
	if err := dimensions(cols, rows); err != nil {
		return Info{}, err
	}
	shell := s.options.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, append([]string{"-l"}, extra...)...)
	cmd.Dir = cwd
	// An owner terminal must never inherit agent identity stamps.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "DYNA_") && !strings.HasPrefix(e, "TERM=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	return s.start(cmd, roomID, sessionID, cwd, shell, cols, rows, true)
}

// OpenCommand runs a host-selected login command directly in a terminal. The
// command exits with the login flow; it does not leave an interactive shell.
func (s *Service) OpenCommand(argv, env []string, cwd string, cols, rows uint16) (Info, error) {
	if len(argv) == 0 || argv[0] == "" {
		return Info{}, errors.New("login command is unavailable")
	}
	if err := dimensions(cols, rows); err != nil {
		return Info{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = childenv.Merge(os.Environ(), env, "xterm-256color")
	return s.start(cmd, "", "", cwd, argv[0], cols, rows, false)
}

func (s *Service) start(cmd *exec.Cmd, roomID, sessionID, cwd, shell string, cols, rows uint16, owner bool) (Info, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Info{}, err
	}
	now := time.Now()
	in := Info{ID: "term_" + hex.EncodeToString(id[:]), RoomID: roomID, SessionID: sessionID, Cwd: cwd, Shell: shell, Cols: cols, Rows: rows, Created: now, LastActivity: now}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Info{}, errors.New("terminal service is closed")
	}
	if len(s.terms) >= s.options.MaxTerminals {
		s.mu.Unlock()
		return Info{}, errors.New("too many terminals; close an existing terminal")
	}
	var file *os.File
	var err error
	cmd = childenv.Clean(cmd)
	start := func() error { file, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows}); return err }
	if owner {
		dataDir := store.DataDir()
		if s.options.SessionRoot != "" {
			dataDir = filepath.Dir(s.options.SessionRoot)
		}
		err = callerid.StartTerminal(context.Background(), cmd, dataDir, start)
	} else {
		err = start()
	}
	if err != nil {
		if file != nil {
			file.Close()
		}
		s.mu.Unlock()
		return Info{}, err
	}
	if err := unix.SetNonblock(int(file.Fd()), true); err != nil {
		file.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.mu.Unlock()
		return Info{}, err
	}
	t := &terminal{info: in, cmd: cmd, file: file, wake: make(chan struct{}), done: make(chan struct{})}
	in.Title = "Terminal"
	in.Ref = roomID
	if sessionID != "" {
		in.Ref = sessionID
	}
	in.Alive = true
	t.info = in
	s.terms[in.ID] = t
	s.mu.Unlock()
	go s.read(t)
	return in, nil
}

func (s *Service) get(id string) (*terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.terms[id]
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}
func (t *terminal) signal() { close(t.wake); t.wake = make(chan struct{}) }
func (s *Service) read(t *terminal) {
	defer close(t.done)
	buf := make([]byte, 16<<10)
	for {
		n, err := t.file.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.offset += uint64(n)
			t.info.LastActivity = time.Now()
			t.buffer = append(t.buffer, buf[:n]...)
			if len(t.buffer) > ScrollbackBytes {
				copy(t.buffer, t.buffer[len(t.buffer)-ScrollbackBytes:])
				t.buffer = t.buffer[:ScrollbackBytes]
			}
			t.signal()
			t.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	err := t.cmd.Wait()
	t.file.Close()
	code := 0
	if err != nil {
		code = t.cmd.ProcessState.ExitCode()
	}
	t.mu.Lock()
	t.info.Exited = true
	t.info.Alive = false
	t.info.ExitCode = code
	t.info.LastActivity = time.Now()
	t.signal()
	t.mu.Unlock()
}

func (s *Service) Write(id string, data []byte) error {
	if len(data) > MaxInputBytes {
		return errors.New("terminal input exceeds 64 KiB")
	}
	t, err := s.get(id)
	if err != nil {
		return err
	}
	t.input.Lock()
	defer t.input.Unlock()
	t.mu.Lock()
	exited := t.info.Exited || t.closed
	t.info.LastActivity = time.Now()
	t.mu.Unlock()
	if exited {
		return errors.New("terminal has exited")
	}
	if err := t.file.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer t.file.SetWriteDeadline(time.Time{})
	_, err = t.file.Write(data)
	return err
}
func (s *Service) Resize(id string, cols, rows uint16) error {
	if err := dimensions(cols, rows); err != nil {
		return err
	}
	t, err := s.get(id)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.info.Exited || t.closed {
		return errors.New("terminal has exited")
	}
	if err := control(t.file, func(fd uintptr) error {
		return unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}); err != nil {
		return err
	}
	t.info.Cols = cols
	t.info.Rows = rows
	t.info.LastActivity = time.Now()
	return nil
}

// Read atomically returns retained output and the next change notification.
// A client must read again when wake closes. Slow clients use bounded memory.
func (s *Service) Read(id string, after uint64) (Event, <-chan struct{}, error) {
	t, err := s.get(id)
	if err != nil {
		return Event{}, nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	start := t.offset - uint64(len(t.buffer))
	reset := after < start || after > t.offset
	if reset {
		after = start
	}
	data := append([]byte{}, t.buffer[after-start:]...)
	return Event{ID: id, Data: data, Start: after, Offset: t.offset, Reset: reset, Exited: t.info.Exited, ExitCode: t.info.ExitCode}, t.wake, nil
}
func (s *Service) List(roomID string) []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Info{}
	for _, t := range s.terms {
		t.mu.Lock()
		select {
		case <-t.done:
			// Closers can still be removing the row after native work ends.
			if t.closed {
				t.mu.Unlock()
				continue
			}
		default:
		}
		if roomID == "" || t.info.RoomID == roomID {
			out = append(out, t.info)
		}
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Close sends SIGKILL to the terminal's session groups, waits for its shell to
// be reaped, and removes the terminal. A background job blocked in kernel I/O
// can remain alive with SIGKILL pending until that I/O completes.
func (s *Service) Close(id string) error {
	s.mu.Lock()
	t := s.terms[id]
	s.mu.Unlock()
	if t == nil {
		return ErrNotFound
	}
	stopTerminal(t)
	<-t.done
	// Keep concurrent closers attached until native work has stopped. A missing
	// terminal must never imply completion while another Close still drains it.
	s.mu.Lock()
	if s.terms[id] == t {
		delete(s.terms, id)
	}
	s.mu.Unlock()
	return nil
}
func stopTerminal(t *terminal) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	if !t.info.Exited {
		// Job-control shells give foreground and background commands separate
		// process groups. End groups that still belong to this terminal session.
		killSessionGroups(t.cmd.Process.Pid)
		// Also handles hosts without a mounted /proc filesystem.
		_ = control(t.file, func(fd uintptr) error {
			group, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
			if err == nil && group > 0 {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			}
			return err
		})
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
	t.file.Close()
	t.signal()
	t.mu.Unlock()
}

// Shutdown closes all shells and waits for their read loops and the idle reaper.
func (s *Service) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stop)
	all := make(map[string]*terminal, len(s.terms))
	for id, t := range s.terms {
		all[id] = t
	}
	s.mu.Unlock()
	for _, t := range all {
		stopTerminal(t)
	}
	for id, t := range all {
		<-t.done
		s.mu.Lock()
		if s.terms[id] == t {
			delete(s.terms, id)
		}
		s.mu.Unlock()
	}
	<-s.reapDone
}
func (s *Service) reap(now time.Time) {
	s.mu.Lock()
	var expired []string
	for id, t := range s.terms {
		t.mu.Lock()
		idle := now.Sub(t.info.LastActivity) > s.options.IdleTimeout
		t.mu.Unlock()
		if idle {
			expired = append(expired, id)
		}
	}
	s.mu.Unlock()
	for _, id := range expired {
		_ = s.Close(id)
	}
}
func (s *Service) reaper() {
	defer close(s.reapDone)
	interval := time.Minute
	if d := s.options.IdleTimeout / 2; d > 0 && d < interval {
		interval = d
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-tick.C:
			if s.options.IdleTimeout > 0 {
				s.reap(now)
			}
		}
	}
}

// Stream waits for changes without keeping a per-client copy of the buffer.
func (s *Service) Stream(ctx context.Context, id string, after uint64, send func(Event) error) error {
	for {
		ev, wake, err := s.Read(id, after)
		if err != nil {
			return err
		}
		if err := send(ev); err != nil {
			return err
		}
		after = ev.Offset
		if ev.Exited {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func control(f *os.File, fn func(uintptr) error) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := raw.Control(func(fd uintptr) { inner = fn(fd) }); err != nil {
		return err
	}
	return inner
}

// KillSessionGroups kills every process group in a private child session.
// Callers must start the child with Setsid and wait for it before releasing state.
func KillSessionGroups(sessionID int) {
	killSessionGroups(sessionID)
}

// killSessionGroups includes background jobs that an interactive shell placed
// outside its own group. A process that deliberately starts a new OS session
// (for example a detached supervisor) owns its separate lifetime.
func killSessionGroups(sessionID int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	groups := map[int]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil || sid != sessionID {
			continue
		}
		group, err := unix.Getpgid(pid)
		if err == nil && group > 0 {
			groups[group] = true
		}
	}
	for group := range groups {
		if group != sessionID {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	}
	_ = syscall.Kill(-sessionID, syscall.SIGKILL)
}
