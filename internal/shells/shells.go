// Package shells runs dyna-managed background commands. A shell is stamped
// with a room and immediate parent session, so its exit can be reported as
// one normal room completion message.
//
// A shell's record lives in the shell_jobs table of dyna.db. Its combined
// stdout and stderr live in <data>/shells/<id>/output.log. A detached supervisor
// process (`dyna shell _run <id>`) owns the command's process group, so a
// shell outlives an agent turn, but closes with its owner unless Keep is set.
package shells

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/procident"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store"
)

// JobEnv names the shell job a process runs in. A session that finishes
// itself from inside a job does not count that job as unfinished work.
const JobEnv = "DYNA_SHELL_JOB"

const (
	// MaxOutputBytes caps output.log; past it, output is dropped (the command
	// keeps running) and one notice is written.
	MaxOutputBytes = 64 << 20
	idPrefix       = "sh_"
)

var idPattern = regexp.MustCompile(`^sh_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`)

// NoDisplayHint is the one stderr line a shell without a display prints.
const NoDisplayHint = "no computer is running, so this shell has no display; " +
	"run dyna computer run, or dyna computer open -- <app>, to start a GUI app on your own computer"

// hintShell and hintScript put the hint on stderr before the command. The hint
// is one argument, and exec hands the remaining arguments to the command
// unchanged, so its PID and exit status are the command's own.
const (
	hintShell  = "/bin/sh"
	hintScript = `printf '%s\n' "$1" >&2; shift; exec "$@"`
)

// ValidID reports whether id has the shell id shape.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Root is <data>/shells.
func Root() string { return filepath.Join(store.DataDir(), "shells") }

// Meta is a shell job's launch record.
type Meta struct {
	ID     string   `json:"id"`
	Name   string   `json:"name,omitempty"`
	Cmd    []string `json:"cmd"`
	Cwd    string   `json:"cwd"`
	Owner  string   `json:"owner,omitempty"`
	Keep   bool     `json:"keep,omitempty"`
	RoomID string   `json:"roomId,omitempty"`
	Room   string   `json:"room,omitempty"`
	// Run names the workflow run whose worker started this job. Workflow
	// workers inherit their parent session's identity, so Owner alone cannot
	// attribute the job to the run a cancel must stop (D-165).
	Run string `json:"run,omitempty"`
	Pid int    `json:"pid,omitempty"` // the command (process group leader)
	// Supervisor is the detached `dyna shell _run` process.
	Supervisor int `json:"supervisor,omitempty"`
	// SupervisorStartTime is Linux /proc/<pid>/stat field 22 (clock ticks).
	SupervisorStartTime uint64    `json:"supervisorStartTime,omitempty"`
	Started             time.Time `json:"started"`
	// BackgroundAt is when a foreground command outlived its limit.
	BackgroundAt *time.Time `json:"backgroundAt,omitempty"`
	// StopBy is who stopped the job: agent, owner or cleanup.
	StopBy string `json:"stopBy,omitempty"`
	// OutputLines is how many lines a finished job wrote to output.log.
	// Save does not write these three fields; their own setters do.
	OutputLines int `json:"outputLines,omitempty"`
}

// Exit records how a shell job ended.
type Exit struct {
	Code   int       `json:"code"`
	Signal string    `json:"signal,omitempty"`
	Error  string    `json:"error,omitempty"`
	Ended  time.Time `json:"ended"`
}

// Info is a shell as listed.
type Info struct {
	Meta
	Status string `json:"status"` // running|exited|lost
	Exit   *Exit  `json:"exit,omitempty"`
}

// Store addresses the shells directory.
type Store struct {
	Root string
	// startTime reads a process start time; nil uses procident.StartTime. A
	// test sets it to act as a system without /proc.
	startTime func(int) (uint64, error)
	// pause waits between attempts to record an exit; nil sleeps. A test sets
	// it so no timer decides the result.
	pause func(time.Duration)
	// failWrite, when set, makes a write fail before it reaches the state
	// store; its argument is "save" or the SQL of the write. A test sets it to
	// act as a store that times out.
	failWrite func(op string) error
	// exitWrite replaces the write of a finished job's exit; nil uses Save. A
	// test sets it to act as a state store that times out.
	exitWrite func(m Meta, ex *Exit) error
	// CommandEnv prepares the environment of a new command. The shell CLI sets
	// it to the display policy of managed shells; nil keeps the supervisor's
	// launch environment, which is the owner display.
	CommandEnv func(ctx context.Context, dir string, m Meta, base []string) (env []string, noDisplay bool)
	// OnStarted, when set, observes a job that started its command and
	// recorded the process. A test waits on it instead of sampling the
	// job's status, so no timer decides when the job is running.
	OnStarted func(pid int)
}

// processStartTime reads pid's start time, or 0 when this system has none. A
// record without one keeps the plain PID probe, and a check that needs it
// fails closed.
func (s *Store) processStartTime(pid int) (uint64, error) {
	if s.startTime != nil {
		return s.startTime(pid)
	}
	return procident.StartTime(pid)
}

func DefaultStore() *Store { return &Store{Root: Root()} }

func (s *Store) dir(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid shell id %q", id)
	}
	return filepath.Join(s.Root, id), nil
}

// StartOptions describes a new shell.
type StartOptions struct {
	// Keep leaves the command running when its owning session closes.
	Keep bool
	Name string
	Cmd  []string
	Cwd  string
	// Owner overrides the owning agent session (default runstore.OwnerFromEnv).
	Owner string
	// Run names the workflow run whose worker starts the job, so a cancel of
	// that run stops it (default runstore.WorkerRunFromEnv).
	Run    string
	RoomID string
	Room   string
	// Hold, when set, is stored with the new job in the same write: a foreground
	// caller waits for the job until then, so its exit does not also wake the owner.
	Hold time.Time
}

// Create records a new shell (not started yet) and returns its id.
func (s *Store) Create(opts StartOptions) (out Meta, err error) {
	err = sessionlife.WithPublication(context.Background(), s.Root, "publication.lock", func() error {
		owner := strings.TrimSpace(opts.Owner)
		if owner == "" {
			owner = runstore.OwnerFromEnv()
		}
		if e := sessionlife.Activate(filepath.Join(filepath.Dir(s.Root), "sessions"), owner); e != nil {
			return e
		}
		if strings.TrimSpace(opts.Run) == "" {
			opts.Run = runstore.WorkerRunFromEnv()
		}
		// The publication lock serializes this check with StopRun's snapshot:
		// a run marked canceling refuses new shells first, and a shell that
		// published first is already in that snapshot (D-165).
		if opts.Run != "" && runstore.Canceling(opts.Run) {
			return fmt.Errorf("run %s is canceled; not starting a new shell", opts.Run)
		}
		out, err = s.create(opts)
		return err
	})
	return out, err
}
func (s *Store) create(opts StartOptions) (Meta, error) {
	if len(opts.Cmd) == 0 || strings.TrimSpace(opts.Cmd[0]) == "" {
		return Meta{}, errors.New("a command is required")
	}
	// Open dyna.db first: its one-time legacy import must not see this
	// job's folder before the job has a row.
	if _, err := s.db(); err != nil {
		return Meta{}, err
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return Meta{}, err
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return Meta{}, fmt.Errorf("working directory %q is not a directory", cwd)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	id := idPrefix + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Meta{}, err
	}
	owner := strings.TrimSpace(opts.Owner)
	if owner == "" {
		owner = runstore.OwnerFromEnv()
	}
	if err := os.WriteFile(filepath.Join(dir, "output.log"), nil, 0o600); err != nil {
		return Meta{}, err
	}
	m := Meta{Keep: opts.Keep, ID: id, Name: opts.Name, Cmd: opts.Cmd, Cwd: cwd, Owner: owner, Run: strings.TrimSpace(opts.Run), RoomID: opts.RoomID, Room: opts.Room, Started: time.Now()}
	return m, s.saveNew(m, opts.Hold)
}

// startBusy opens the error of a job that could not begin because the state
// store did not answer in time (D-220).
const startBusy = "job did not start: the state store was busy; retry"

// startError says why a job did not start. A state-store timeout gets the
// plain busy message, never a bare "context deadline exceeded".
func startError(err error) error {
	if timedOut(err) {
		return fmt.Errorf("%s: %w", startBusy, err)
	}
	return fmt.Errorf("job did not start: %w", err)
}

// startSaveTries and startSaveWait bound how long a supervisor keeps trying to
// record itself before its command starts.
const (
	startSaveTries = 3
	startSaveWait  = 2 * time.Second
)

// saveStart records the supervisor's identity, trying again while the state
// store times out. A final failure reads as a job that did not start.
func (s *Store) saveStart(m Meta) error {
	pause := s.pause
	if pause == nil {
		pause = time.Sleep
	}
	var err error
	for i := 0; i < startSaveTries; i++ {
		if err = s.Save(m, nil); !timedOut(err) {
			break
		}
		if i < startSaveTries-1 {
			pause(startSaveWait)
		}
	}
	if err != nil {
		return startError(err)
	}
	return nil
}

// StartJob records a new job together with its exit hold, and calls spawn to
// start its supervisor. If a step fails, the job is removed so that no lost job
// and no command are left behind, and the error says the job did not start
// (D-220). The row is deleted first, with the same retries a supervisor makes,
// and the folder stays until that delete has succeeded. The returned Meta keeps
// the job's ID on failure, so a caller can forget it.
func (s *Store) StartJob(opts StartOptions, hold time.Time, spawn func(Meta) error) (Meta, error) {
	opts.Hold = hold
	meta, err := s.Create(opts)
	if err == nil {
		err = spawn(meta)
	}
	if err == nil {
		return meta, nil
	}
	if ValidID(meta.ID) {
		if derr := s.discardStart(meta.ID); derr != nil {
			// The row may stay, so keep its folder and say so.
			return meta, errors.Join(startError(err), fmt.Errorf("the job %s could not be removed: %w", meta.ID, derr))
		}
	}
	return meta, startError(err)
}

// discardStart deletes a job that did not start, then its folder. A write that
// timed out earlier may still commit, so the delete queues behind it and is
// tried again while the store times out.
func (s *Store) discardStart(id string) error {
	pause := s.pause
	if pause == nil {
		pause = time.Sleep
	}
	var err error
	for i := 0; i < startSaveTries; i++ {
		if _, err = s.exec(id, `DELETE FROM shell_jobs WHERE id=?`, id); !timedOut(err) {
			break
		}
		if i < startSaveTries-1 {
			pause(startSaveWait)
		}
	}
	if err != nil {
		return err
	}
	dir, err := s.dir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// injected returns the error a test made a write fail with, or nil.
func (s *Store) injected(op string) error {
	if s.failWrite != nil {
		return s.failWrite(op)
	}
	return nil
}

// Supervise runs the shell's command to completion (the detached `_run`
// process). It records the pid, streams output to output.log, and writes
// exit.json.
func (s *Store) Supervise(ctx context.Context, id string) error {
	dir, err := s.dir(id)
	if err != nil {
		return err
	}
	m, err := s.ReadMeta(id)
	if err != nil {
		return err
	}
	if err := awaitLaunch(); err != nil {
		return err
	}
	m.Supervisor = os.Getpid()
	m.SupervisorStartTime, err = s.processStartTime(m.Supervisor)
	if err != nil {
		return fmt.Errorf("record supervisor identity: %w", err)
	}
	if err := s.saveStart(m); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if !m.Keep {
		closed, err := s.ownerClosed(m.Owner)
		if err != nil {
			return err
		}
		if closed {
			_, _ = out.WriteString("\n[dyna: shell stopped before start because its owner is closed or missing]\n")
			return s.Save(m, &Exit{Code: -1, Error: "owner is closed or missing", Ended: time.Now()})
		}
		// A supervisor delayed past the point where its job reads as lost must
		// not launch its worker's command behind a cancelled run (D-165).
		if m.Run != "" && runstore.Canceling(m.Run) {
			_, _ = out.WriteString("\n[dyna: shell stopped before start because its run is canceled]\n")
			return s.Save(m, &Exit{Code: -1, Error: "run is canceled", Ended: time.Now()})
		}
	}
	// A stop that arrived while the supervisor waited for dyna.db wins: the
	// command never starts, so a caller that saw the job stop cannot run it twice.
	if ctx.Err() != nil {
		_, _ = out.WriteString("\n[dyna: shell stopped before start]\n")
		if err := s.Save(m, &Exit{Code: -1, Error: "stopped before start", Ended: time.Now()}); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, exitedMarker), nil, 0o600)
	}
	cmd := exec.Command(m.Cmd[0], m.Cmd[1:]...)
	cmd.Dir = m.Cwd
	env, noDisplay := home.LaunchEnv(), false
	if s.CommandEnv != nil {
		env, noDisplay = s.CommandEnv(ctx, dir, m, env)
	}
	if noDisplay {
		cmd = exec.Command(hintShell, append([]string{"-c", hintScript, hintShell, NoDisplayHint}, m.Cmd...)...)
		cmd.Dir = m.Cwd
	}
	// Without a terminal, a pager or color codes only get in the way.
	cmd.Env = append(env, "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1", JobEnv+"="+m.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = nil
	if err := childenv.Clean(cmd).Start(); err != nil {
		_ = s.Save(m, &Exit{Code: -1, Error: err.Error(), Ended: time.Now()})
		return err
	}
	redact := newRedactor(cmd.Env)
	m.Pid = cmd.Process.Pid
	_ = s.Save(m, nil)
	if s.OnStarted != nil {
		s.OnStarted(m.Pid)
	}

	copied := make(chan struct{})
	var lines int
	grew := &outputEvents{send: func() { s.Notify(id) }}
	go func() {
		defer close(copied)
		r := bufio.NewReaderSize(pr, 64*1024)
		var written int64
		capped := false
		for {
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				if written < MaxOutputBytes {
					n, _ := out.WriteString(redact.line(line))
					written += int64(n)
					lines++
					grew.grew()
				} else if !capped {
					capped = true
					_, _ = out.WriteString("\n[dyna: output.log reached its cap; further output dropped]\n")
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-waitErr:
	case <-ctx.Done():
		_ = syscall.Kill(-m.Pid, syscall.SIGTERM)
		select {
		case werr = <-waitErr:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-m.Pid, syscall.SIGKILL)
			werr = <-waitErr
		}
	}
	pw.Close()
	<-copied
	grew.stop()
	ex := Exit{Ended: time.Now()}
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
				ex.Code, ex.Signal = -1, st.Signal().String()
			} else {
				ex.Code = ee.ExitCode()
			}
		} else {
			ex.Code, ex.Error = -1, werr.Error()
		}
	}
	// The exit goes first: a failed line count must not lose it, or the job
	// shows as lost once this process ends.
	if err := s.saveExit(m, ex); err != nil {
		return err
	}
	return errors.Join(s.setOutputLines(id, lines), os.WriteFile(filepath.Join(dir, exitedMarker), nil, 0o600))
}

// exitSaveTries and exitSaveWait bound how long a supervisor keeps trying to
// record a finished job's exit while dyna.db is too busy to take the write
// (D-207): about 10 minutes, after the 10 s each attempt already waits.
const (
	exitSaveTries = 60
	exitSaveWait  = 10 * time.Second
)

// saveExit records an exit with save. A write that timed out waiting for the
// state store is tried again, since the command's result must not be lost to a
// busy writer. When the tries run out, the error names the timeout and the exit
// the command had, so the job is not mistaken for a dead supervisor.
func (s *Store) saveExit(m Meta, ex Exit) error {
	pause := s.pause
	if pause == nil {
		pause = time.Sleep
	}
	var err error
	for i := 0; i < exitSaveTries; i++ {
		if err = s.writeExit(m, &ex); !timedOut(err) {
			return err
		}
		pause(exitSaveWait)
	}
	return fmt.Errorf("state store timed out recording the job exit (code %d, signal %q): %w", ex.Code, ex.Signal, err)
}

// writeExit stores the exit; a test replaces it through Store.exitWrite.
func (s *Store) writeExit(m Meta, ex *Exit) error {
	if s.exitWrite != nil {
		return s.exitWrite(m, ex)
	}
	return s.Save(m, ex)
}

// timedOut reports whether a state write ran out of time. The database driver
// may flatten the context error to its text, so the text counts too.
func timedOut(err error) bool {
	return err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error()))
}

// ReadMeta loads a job's launch record.
func (s *Store) ReadMeta(id string) (Meta, error) {
	m, _, err := s.load(id)
	return m, err
}

// ReadExit loads a job's exit, if it has ended.
func (s *Store) ReadExit(id string) (*Exit, error) {
	_, ex, err := s.load(id)
	return ex, err
}

// Info resolves a shell's live status from its current record.
func (s *Store) Info(id string) (Info, error) {
	m, ex, err := s.load(id)
	if err != nil {
		return Info{}, err
	}
	return s.status(Info{Meta: m, Exit: ex}), nil
}

// status resolves a shell's live status from its canonical record.
func (s *Store) status(in Info) Info {
	switch {
	case in.Exit != nil:
		in.Status = "exited"
	case in.SupervisorAlive() && s.sameSupervisor(in):
		in.Status = "running"
	case in.Supervisor == 0 && (time.Since(in.Started) < 10*time.Second || s.launchedSupervisor(in.ID) > 0):
		// A live supervisor that has not recorded itself yet may be waiting
		// for dyna.db's write lock; it is starting, not lost.
		in.Status = "starting"
	default:
		// The supervisor died without recording an exit; re-check once in
		// case it recorded one just now.
		if ex, _ := s.ReadExit(in.ID); ex != nil {
			in.Exit, in.Status = ex, "exited"
		} else {
			in.Status = "lost"
		}
	}
	return in
}

// sameSupervisor reports whether the process at the recorded supervisor PID
// is the supervisor, not another process that reused the number (D-201). A
// record or system without a start time keeps the plain PID probe.
func (s *Store) sameSupervisor(in Info) bool {
	if in.SupervisorStartTime == 0 {
		return true
	}
	now, err := s.processStartTime(in.Supervisor)
	return err != nil || now == 0 || now == in.SupervisorStartTime
}

// SupervisorAlive reports whether the job's recorded supervisor still runs.
func (in Info) SupervisorAlive() bool {
	return in.Supervisor > 0 && syscall.Kill(in.Supervisor, 0) == nil
}

// Resolve finds a shell by id or by its name (newest wins).
func (s *Store) Resolve(ref string) (Info, error) {
	if ValidID(ref) {
		return s.Info(ref)
	}
	list, err := s.List(true)
	if err != nil {
		return Info{}, err
	}
	for _, in := range list {
		if in.Name == ref {
			return in, nil
		}
	}
	return Info{}, fmt.Errorf("no shell with id or name %q", ref)
}

// List returns shells newest first; all=false keeps running ones only.
func (s *Store) List(all bool) ([]Info, error) {
	where := ""
	if !all {
		where = "WHERE ended IS NULL"
	}
	list, err := s.loadAll(where)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, in := range list {
		in = s.status(in)
		if all || in.Status == "running" || in.Status == "starting" {
			out = append(out, in)
		}
	}
	return out, nil
}

// ListOwner returns every shell the owner started, newest first.
func (s *Store) ListOwner(owner string) ([]Info, error) {
	list, err := s.loadAll("WHERE owner=?", owner)
	for i := range list {
		list[i] = s.status(list[i])
	}
	return list, err
}

// ListRoom returns only the room's shells, newest first.
func (s *Store) ListRoom(roomID string) ([]Info, error) {
	list, err := s.loadAll("WHERE room_id=?", roomID)
	for i := range list {
		list[i] = s.status(list[i])
	}
	return list, err
}

// Tail returns the last n lines of output.log.
func (s *Store) Tail(id string, n int) (string, error) {
	dir, err := s.dir(id)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(dir, "output.log"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	// A window of lines holds about 1 KB each; a longer line is cut at its start.
	window := int64(256 * 1024)
	if n > 0 {
		window = min(window, max(int64(n)*1024, 4096))
	}
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(buf), "\r\n", "\n"), "\n"), "\n")
	if off > 0 && len(lines) > 1 {
		lines = lines[1:] // the window starts inside a line
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// OutputPath is the shell's output.log.
func (s *Store) OutputPath(id string) (string, error) {
	dir, err := s.dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "output.log"), nil
}

// ClaimNotice waits while a foreground caller still waits for this finished
// job, then claims its exit. True means the owner has not seen the exit and
// the caller must tell it.
func (s *Store) ClaimNotice(id string) (bool, error) {
	for {
		until, delivered, err := s.hold(id)
		if err != nil || delivered {
			return false, err
		}
		left := time.Until(until)
		if left <= 0 {
			return s.ClaimExit(id)
		}
		// The waiter claims the exit when it sees it, or its hold lapses if
		// its process dies. This runs only in the detached supervisor.
		time.Sleep(min(left, 250*time.Millisecond))
	}
}

// exitedMarker is created after the exit is recorded, so waiters wake.
const exitedMarker = "exited"

// Wait blocks until the shell ends, ready reports true, ctx ends, or the
// timeout passes (0 = no limit). ready may be nil. It reports whether the
// shell ended or became ready. Waiters wake on job directory changes.
func (s *Store) Wait(ctx context.Context, id string, timeout time.Duration, ready func() bool) (Info, bool, error) {
	dir, err := s.dir(id)
	if err != nil {
		return Info{}, false, err
	}
	w, err := watchDir(dir)
	if err != nil {
		return Info{}, false, err
	}
	defer w.close()
	// A cancelled caller must not wait out the current poll step.
	defer context.AfterFunc(ctx, w.wake)()
	deadline := time.Now().Add(timeout)
	for {
		in, err := s.Info(id)
		if err != nil {
			return Info{}, false, err
		}
		if in.Status == "exited" {
			return in, true, nil
		}
		if in.Status == "lost" || ready != nil && ready() {
			return in, true, nil
		}
		if err := ctx.Err(); err != nil {
			return in, false, err
		}
		// A supervisor that dies without a record changes no file, so
		// re-check its liveness at least once a second.
		step := time.Second
		if timeout > 0 {
			left := time.Until(deadline)
			if left <= 0 {
				return in, false, nil
			}
			step = min(step, left)
		}
		if err := w.next(step); err != nil {
			return in, false, err
		}
	}
}

// Stop ends a running shell: SIGTERM to its process group, SIGKILL after 5 s.
// For an exited shell, it also stops a verified supervisor stuck in completion delivery.
// by says who asked: agent, owner or cleanup.
func (s *Store) Stop(id, by string) (Info, error) {
	in, err := s.Info(id)
	if err != nil {
		return Info{}, err
	}
	// The supervisor publishes its identity before it starts the child.
	// Wait for that child PID too, so Stop cannot miss a shell during startup.
	for deadline := time.Now().Add(10 * time.Second); (in.Status == "starting" || in.Status == "running" && in.Pid <= 0) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		in, err = s.Info(id)
		if err != nil {
			return Info{}, err
		}
	}
	if in.Status == "exited" {
		return in, s.stopExitedSupervisor(in)
	}
	if in.Status == "starting" {
		// Still waiting to record itself. SIGTERM makes it record a stop
		// instead of starting the command.
		if pid := s.launchedSupervisor(id); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		in, _, err = s.Wait(context.Background(), id, 10*time.Second, nil)
		return in, err
	}
	if in.Status == "lost" {
		// The supervisor died, but the command and its children may run on.
		// Only processes stamped with this job's ID are ours to end (D-190).
		logErr := s.logStop(id, "supervisor is gone; sending SIGKILL to the job's processes")
		found, err := stopOrphans(id)
		if err != nil {
			return in, errors.Join(logErr, err)
		}
		if !found {
			return in, logErr
		}
		in.Exit = &Exit{Code: -1, Signal: "terminated", Error: "supervisor lost; processes stopped", Ended: time.Now()}
		in.Status = "exited"
		return in, errors.Join(logErr, s.markStopped(id, by), s.Save(in.Meta, in.Exit))
	}
	if in.Status != "running" || in.Pid <= 0 {
		return in, nil
	}
	// A log failure must not leave the process running.
	logErr := errors.Join(s.markStopped(id, by), s.logStop(id, "stop requested; sending SIGTERM to the process group"))
	_ = syscall.Kill(-in.Pid, syscall.SIGTERM)
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if syscall.Kill(in.Pid, 0) != nil {
			break
		}
	}
	if syscall.Kill(in.Pid, 0) == nil {
		logErr = errors.Join(logErr, s.logStop(id, "shell did not stop within 5 seconds; sending SIGKILL"))
		_ = syscall.Kill(-in.Pid, syscall.SIGKILL)
	}
	in, _, err = s.Wait(context.Background(), id, 10*time.Second, nil)
	return in, errors.Join(logErr, err)
}

// Spawn launches the detached supervisor through `exe shell _spawn <id>`,
// which starts it and exits at once, so the supervisor is reparented to init.
// A harness that kills an MCP server's whole live process tree when a run
// ends cannot reach an orphan. A launch error comes back as _spawn's error.
func Spawn(exe, id string) error {
	cmd := exec.Command(exe, "shell", "_spawn", id)
	cmd.Env = home.LaunchEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := childenv.Clean(cmd).Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("start shell supervisor: %s", msg)
		}
		return fmt.Errorf("start shell supervisor: %w", err)
	}
	return nil
}

// supervisorLog holds a supervisor's own errors, in the job folder.
const supervisorLog = "supervisor.log"

// SupervisorErrors returns the end of a supervisor's own error output, or ""
// when it wrote none.
func (s *Store) SupervisorErrors(id string) string {
	dir, err := s.dir(id)
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, supervisorLog))
	if err != nil {
		return ""
	}
	if len(raw) > 2048 {
		raw = raw[len(raw)-2048:]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
}

// StartSupervisor starts exe (dyna) `shell _run <id>` in its own session and
// returns once it runs; `dyna shell _spawn` calls it. A job a session owns
// runs in its own agent scope, recorded for that session (requirements
// item 3), so its commands' engine calls act for the session.
func StartSupervisor(exe, id string) error {
	m, err := DefaultStore().ReadMeta(id)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "shell", "_run", id)
	// The supervisor restores the owner's roots for itself and starts the
	// command with this launch environment.
	cmd.Env = home.LaunchEnv()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	// A supervisor that fails before it records an exit leaves its error
	// here, so a lost job can say why.
	dir, err := DefaultStore().dir(id)
	if err != nil {
		return err
	}
	errLog, err := os.OpenFile(filepath.Join(dir, supervisorLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer errLog.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, errLog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The supervisor starts its command only after the launcher has recorded
	// the supervisor: it waits on this pipe for the launcher's go-ahead, and an
	// end without one means the launch failed.
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer gateWrite.Close()
	cmd.ExtraFiles = []*os.File{gateRead}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", launchGateEnv, 3))
	if sessionlife.IsSessionID(m.Owner) {
		err = callerid.Start(context.Background(), childenv.Clean(cmd), callerid.Launch{DataDir: store.DataDir(), Name: m.Owner, Actor: callerid.Identity{Kind: callerid.Session, Session: m.Owner}, Launcher: "shell"})
	} else {
		err = childenv.Clean(cmd).Start()
	}
	gateRead.Close() // the supervisor holds its own copy
	if err != nil {
		return err
	}
	// Record the supervisor outside dyna.db, which may be busy: status must
	// tell a supervisor still waiting to record itself from a dead one.
	started, _ := procident.StartTime(cmd.Process.Pid)
	launched := fmt.Sprintf("%d %d\n", cmd.Process.Pid, started)
	if err := os.WriteFile(filepath.Join(dir, supervisorPID), []byte(launched), 0o600); err != nil {
		// Closing the gate unsent keeps the command from ever starting: the
		// supervisor ends by itself, and is waited for. Every process stamped
		// with the job is stopped too, in case the command had started anyway.
		gateWrite.Close()
		_, _ = cmd.Process.Wait()
		_, cleanup := stopOrphans(id)
		return errors.Join(err, cleanup)
	}
	if _, err := gateWrite.Write([]byte{'g'}); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
		return err
	}
	return cmd.Process.Release()
}

// launchGateEnv names the file descriptor on which a supervisor waits for its
// launcher's go-ahead before it starts its command.
const launchGateEnv = "DYNA_SHELL_LAUNCH_GATE"

// awaitLaunch blocks until the launcher has recorded this supervisor. A
// supervisor started without a gate (a test, or an older launcher) goes on at
// once. If the launcher closes the gate without the go-ahead, the command is
// never started.
func awaitLaunch() error {
	v := os.Getenv(launchGateEnv)
	if v == "" {
		return nil
	}
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid %s %q", launchGateEnv, v)
	}
	gate := os.NewFile(uintptr(fd), "launch gate")
	defer gate.Close()
	var b [1]byte
	if n, _ := gate.Read(b[:]); n != 1 {
		return errors.New("the launcher did not record this supervisor; the command was not started")
	}
	return nil
}

// supervisorPID holds the launched supervisor's PID and start time.
const supervisorPID = "supervisor.pid"

// launchedSupervisor returns the PID of the job's launched supervisor while
// that same process is alive, or 0.
func (s *Store) launchedSupervisor(id string) int {
	dir, err := s.dir(id)
	if err != nil {
		return 0
	}
	var pid int
	var started uint64
	raw, err := os.ReadFile(filepath.Join(dir, supervisorPID))
	if err != nil {
		return 0
	}
	if _, err := fmt.Sscan(string(raw), &pid, &started); err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0
	}
	// A reused PID is another process.
	if now, err := s.processStartTime(pid); started != 0 && (err != nil || now != started) {
		return 0
	}
	return pid
}

// StopOwner stops non-keep shells belonging to exactly this session.
// Session metadata is already closed before this runs. Stop also waits for
// supervisors that have not yet published their process ID.
func (s *Store) StopOwner(owner string) error {
	if owner == "" {
		return nil
	}
	list, err := s.List(false)
	if err != nil {
		return err
	}
	var errs []error
	for _, in := range list {
		if in.Owner != owner || in.Keep {
			continue
		}
		if err := s.logStop(in.ID, "owner "+owner+" is closed or missing; stopping shell"); err != nil {
			errs = append(errs, err)
		}
		if _, err := s.Stop(in.ID, "cleanup"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StopRun stops the non-keep shells that workers of runID started. A
// cancelled run ends its workers, so their jobs stop the way a closed
// session's do (D-165). The snapshot shares Create's publication lock, and
// cancellation is marked before this runs, so a worker either published
// before the snapshot or is refused afterwards. Stops wait outside the lock.
func (s *Store) StopRun(runID string) error {
	if runID == "" {
		return nil
	}
	var list []Info
	if err := sessionlife.WithPublication(context.Background(), s.Root, "publication.lock", func() error {
		var err error
		list, err = s.List(false)
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, in := range list {
		if in.Run != runID || in.Keep {
			continue
		}
		if err := s.logStop(in.ID, "run "+runID+" is canceled; stopping shell"); err != nil {
			errs = append(errs, err)
		}
		if _, err := s.Stop(in.ID, "cleanup"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep stops shells whose session owner has closed or no longer exists.
// Empty and external harness owners do not identify a Dyna session.
// Unreadable or malformed owner records return an error and do not stop work.
func (s *Store) Sweep() error {
	list, err := s.List(false)
	if err != nil {
		return err
	}
	owners := map[string]bool{}
	var errs []error
	for _, in := range list {
		if in.Keep || owners[in.Owner] {
			continue
		}
		closed, err := s.ownerClosed(in.Owner)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if closed {
			owners[in.Owner] = true
			errs = append(errs, s.StopOwner(in.Owner))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) ownerClosed(owner string) (bool, error) {
	if !runstore.IsSessionDirID(owner) {
		return false, nil
	}
	meta, err := sessionlife.Read(filepath.Join(filepath.Dir(s.Root), "sessions"), owner)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if meta.ID != owner {
		return false, fmt.Errorf("cannot verify shell owner %s: stored session ID does not match", owner)
	}
	return meta.IsClosed(), nil
}

func (s *Store) logStop(id, reason string) error {
	path, err := s.OutputPath(id)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n[dyna: %s]\n", reason)
	return err
}
