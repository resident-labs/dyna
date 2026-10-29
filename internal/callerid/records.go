package callerid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// recordsSchema keeps a workflow agent's number in the worker column as text
// ("" in old rows); reads cast it to an integer, so "" is 0, the run itself.
const recordsSchema = `CREATE TABLE IF NOT EXISTS caller_records (
 unit TEXT NOT NULL, pid INTEGER NOT NULL, start INTEGER NOT NULL,
 actor TEXT NOT NULL, session TEXT NOT NULL, run TEXT NOT NULL, worker TEXT NOT NULL,
 launcher TEXT NOT NULL, recorded INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS caller_records_unit ON caller_records(unit);
CREATE INDEX IF NOT EXISTS caller_records_process ON caller_records(pid, start);`

// recordsKept is how long a record outlives its process. A scope can outlive
// its runner, for example while a background job started in it still runs.
const recordsKept = 24 * time.Hour

// Store keeps the launch records in dyna.db: the scope and the process that
// each launch started, and the identity they act for.
type Store struct{ db *sql.DB }

// StoreFormat says how the binary that records a launch stores session state.
// 3 means turn prompts use shared blobs in dyna.db.
// Records made by an earlier release have format 0.
const StoreFormat = 3

// formatSchema adds the launching binary's StoreFormat. It migrates under its
// own key, so an earlier release still finds caller_records at the version it
// knows; its inserts name their columns and leave format at 0.
const formatSchema = `ALTER TABLE caller_records ADD COLUMN format INTEGER NOT NULL DEFAULT 0`

// Migrate brings the launch records in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := OpenStore(dataDir)
	return err
}

// OpenStore opens the launch records in dataDir.
func OpenStore(dataDir string) (Store, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return Store{}, err
	}
	err = database.Migrate(db, "caller_records.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), recordsSchema)
		return err
	})
	if err != nil {
		return Store{db}, err
	}
	err = database.Migrate(db, "caller_records.format", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), formatSchema)
		return err
	})
	return Store{db}, err
}

func (s Store) ScopeIdentity(unit string) (Identity, bool, error) {
	if unit == "" {
		return Identity{}, false, nil
	}
	return s.identity(`SELECT actor, session, run, CAST(worker AS INTEGER) FROM caller_records WHERE unit=? LIMIT 1`, unit)
}

func (s Store) ProcessIdentity(p Process) (Identity, bool, error) {
	return s.identity(`SELECT actor, session, run, CAST(worker AS INTEGER) FROM caller_records WHERE pid=? AND start=? LIMIT 1`, p.PID, int64(p.Start))
}

// Launched is a process's launch record: who it acts for, and the
// StoreFormat of the binary that launched it.
type Launched struct {
	Identity
	Format int
}

// ProcessLaunch returns p's launch record.
func (s Store) ProcessLaunch(p Process) (Launched, bool, error) {
	var l Launched
	err := retryRead(func() error {
		return s.db.QueryRow(`SELECT actor, session, run, CAST(worker AS INTEGER), format FROM caller_records WHERE pid=? AND start=? LIMIT 1`, p.PID, int64(p.Start)).
			Scan(&l.Kind, &l.Session, &l.Run, &l.Agent, &l.Format)
	})
	if err == sql.ErrNoRows {
		return Launched{}, false, nil
	}
	return l, err == nil, err
}

// WorkflowDaemonAlive reports whether run still has its recorded coordinator.
// The process start time prevents a reused PID from keeping a dead run alive.
func (s Store) WorkflowDaemonAlive(ctx context.Context, run string) (bool, error) {
	var processes []Process
	err := database.Retry(ctx, func() error {
		processes = nil
		rows, err := s.db.QueryContext(ctx, `SELECT pid,start FROM caller_records
WHERE run=? AND actor='worker' AND session='' AND CAST(worker AS INTEGER)=0 AND launcher='workflow'`, run)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p Process
			var start int64
			if err := rows.Scan(&p.PID, &start); err != nil {
				rows.Close()
				return err
			}
			if p.PID <= 0 || start <= 0 {
				rows.Close()
				return fmt.Errorf("run %s has an invalid workflow launch record", run)
			}
			p.Start = uint64(start)
			processes = append(processes, p)
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	if err != nil {
		return false, err
	}
	for _, recorded := range processes {
		current, err := ProcessOf(recorded.PID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if current == recorded {
			return true, nil
		}
	}
	return false, nil
}

func (s Store) identity(query string, args ...any) (Identity, bool, error) {
	var id Identity
	err := retryRead(func() error {
		return s.db.QueryRow(query, args...).Scan(&id.Kind, &id.Session, &id.Run, &id.Agent)
	})
	if err == sql.ErrNoRows {
		return Identity{}, false, nil
	}
	return id, err == nil, err
}

// launchReadWait bounds the retries of a launch-records read that SQLite
// answers with a transient lock error.
var launchReadWait = 5 * time.Second

// retryRead runs one launch-records read and retries SQLite's transient lock
// answers, so a loaded machine cannot fail the caller's whole call.
func retryRead(read func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), launchReadWait)
	defer cancel()
	return database.Retry(ctx, read)
}

// record stores one launch made at time at. It drops the records of
// processes that ended more than recordsKept before, so the table holds only
// live work.
func (s Store) record(ctx context.Context, at time.Time, unit string, p Process, actor Identity, launcher string) error {
	event := hub.Event{Topic: "callers"}
	if actor.Kind == Worker && actor.Agent == 0 {
		event.Key = actor.Run
	}
	return state.For(s.db).Write(ctx, event, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT rowid, pid, start FROM caller_records WHERE recorded < ?`, at.Add(-recordsKept).Unix())
		if err != nil {
			return err
		}
		var ended []int64
		for rows.Next() {
			var id int64
			var old Process
			var start int64
			if err := rows.Scan(&id, &old.PID, &start); err != nil {
				rows.Close()
				return err
			}
			old.Start = uint64(start)
			if live, _, err := readStat("/proc", old.PID); err != nil || live != old {
				ended = append(ended, id)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ended {
			if _, err := conn.ExecContext(ctx, `DELETE FROM caller_records WHERE rowid=?`, id); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO caller_records(unit,pid,start,actor,session,run,worker,launcher,recorded,format) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			unit, p.PID, int64(p.Start), actor.Kind, actor.Session, actor.Run, actor.Agent, launcher, at.Unix(), StoreFormat)
		return err
	})
}

// Launch says what a started process acts for. Launcher names the launch
// path, for example "turn", "workflow" or "shell". Room is the canonical
// room id the work belongs to; it puts the launch in the room's own slice.
type Launch struct {
	DataDir  string
	Name     string // labels the scope, for example the session ID
	Actor    Identity
	Launcher string
	Room     string
}

// check refuses a launch that would record a wrong identity.
func (l Launch) check() error { return l.checkActor(false) }

func (l Launch) checkActor(owner bool) error {
	a := l.Actor
	switch {
	case a.Kind == Session && a.Session != "" && a.Run == "" && a.Agent == 0:
	case a.Kind == Worker && a.Run != "" && a.Session == "" && a.Agent == 0:
	case a.Kind == Owner && owner && a.Session == "" && a.Run == "" && a.Agent == 0:
	default:
		return fmt.Errorf("a %s launch cannot be recorded for %+v", l.Launcher, a)
	}
	return nil
}

// LaunchGate names the inherited pipe that holds a started dyna process until
// its launch is recorded. It names a file descriptor, never an identity.
const LaunchGate = "DYNA_LAUNCH_GATE"

// Start starts cmd in a new agent scope, when the systemd user manager runs,
// and records the scope and the process for l.Actor, so the engine can
// tell that actor's callers (requirements item 3). Every launch of work
// for a session uses Start, and cmd runs dyna. The child waits in
// WaitForLaunch until the record commits, so its first engine call is never
// refused. When the record fails, Start stops the process.
func Start(ctx context.Context, cmd *exec.Cmd, l Launch) error {
	if err := l.check(); err != nil {
		return err
	}
	// TODO: legacy/shim remove later — launchers outside the engine (the CLI and detached processes) write their own launch records; remove when item 2 moves each launch into the engine, which then records it.
	unit := agentslice.WrapRoom(cmd, l.Name, l.Room)
	gate, held, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, gate)
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("%s=%d", LaunchGate, 2+len(cmd.ExtraFiles)))
	err = cmd.Start()
	gate.Close()
	if err != nil {
		held.Close()
		return err
	}
	s, err := OpenStore(l.DataDir)
	if err == nil {
		var p Process
		if p, _, err = readStat("/proc", cmd.Process.Pid); err == nil {
			err = s.record(ctx, time.Now(), unit, p, l.Actor, l.Launcher)
		}
	}
	if err = release(held, err); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("record the %s launch: %w", l.Launcher, err)
	}
	return nil
}

// Isolate puts cmd in its own agent scope without recording a caller. Use it
// only for a command that never calls the engine, such as a harness CLI's
// version check, whose scope keeps a daemon it starts out of the engine's unit.
// The returned function stops the scope.
func Isolate(cmd *exec.Cmd, name string) (stop func()) {
	unit := agentslice.Wrap(cmd, name)
	return func() { agentslice.Stop(unit) }
}

// StartTerminal holds an owner terminal's requested shell until its exact
// process record commits. Only the engine's authenticated terminal path may
// use it; the shared engine cgroup never receives owner authority.
func StartTerminal(ctx context.Context, cmd *exec.Cmd, dataDir string, start func() error) error {
	if !authority.IsEngine() {
		return errors.New("only the verified engine may start an owner terminal")
	}
	if len(cmd.ExtraFiles) != 0 {
		return errors.New("owner terminal cannot inherit extra files")
	}
	gate, held, err := os.Pipe()
	if err != nil {
		return err
	}
	defer gate.Close()
	defer held.Close()
	args := append([]string{"/bin/sh", "-c", `IFS= read -r ready <&3 && [ "$ready" = ready ] && exec 3<&- && exec "$@"`, "dyna-terminal", cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args, cmd.ExtraFiles = "/bin/sh", args, []*os.File{gate}
	if err := start(); err != nil {
		return err
	}
	process, err := ProcessOf(cmd.Process.Pid)
	if err == nil {
		err = recordProcess(ctx, process, Launch{DataDir: dataDir, Actor: Identity{Kind: Owner}, Launcher: "terminal"})
	}
	if err == nil {
		_, err = held.Write([]byte("ready\n"))
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("record the owner terminal launch: %w", err)
	}
	return nil
}

// RecordStarted records a process that its launcher already started and that
// does not run dyna, so it cannot wait in WaitForLaunch: a workflow agent's
// harness CLI (requirements item 3). A dyna command that the process runs
// before the record commits resolves through its ancestors to the
// launcher's own record, for an agent its run with agent 0. A harness takes
// seconds to start before its agent runs a command, so in practice the
// record is in place.
func RecordStarted(ctx context.Context, pid int, l Launch) error {
	if err := l.check(); err != nil {
		return err
	}
	return recordStarted(ctx, pid, l)
}

// RecordHook records the live anchor of a stored hook while processguard's
// native gate is closed. Only the engine may use this path, including for an
// explicit request acting for its authenticated caller. Generic launchers
// reject owner identities.
func RecordHook(ctx context.Context, process Process, dataDir, name string, actor Identity) error {
	if !authority.IsEngine() {
		return errors.New("only the verified engine may record a hook launch")
	}
	l := Launch{DataDir: dataDir, Actor: actor, Launcher: "hook " + name}
	if err := l.checkActor(true); err != nil {
		return err
	}
	current, _, err := readStat("/proc", process.PID)
	if err != nil {
		return err
	}
	if current != process {
		return errors.New("hook anchor process changed before its launch was recorded")
	}
	return recordProcess(ctx, process, l)
}

func recordStarted(ctx context.Context, pid int, l Launch) error {
	p, _, err := readStat("/proc", pid)
	if err != nil {
		return err
	}
	return recordProcess(ctx, p, l)
}

func recordProcess(ctx context.Context, p Process, l Launch) error {
	s, err := OpenStore(l.DataDir)
	if err != nil {
		return err
	}
	return s.record(ctx, time.Now(), "", p, l.Actor, l.Launcher)
}

// release opens the gate of a held child once its record committed, and
// closes it. After a failed record the child gets no byte and exits in
// WaitForLaunch. A child that already exited needs no byte.
func release(held *os.File, recorded error) error {
	defer held.Close()
	if recorded != nil {
		return recorded
	}
	if _, err := held.Write([]byte{1}); err != nil && !errors.Is(err, syscall.EPIPE) {
		return err
	}
	return nil
}

// WaitForLaunch holds a process that Start launched until its launcher has
// recorded it. A process the launcher could not record exits. Every dyna
// process calls it first; it returns at once for any other start.
func WaitForLaunch() {
	value, ok := os.LookupEnv(LaunchGate)
	if !ok {
		return
	}
	// Children of this process are not held.
	os.Unsetenv(LaunchGate)
	fd, err := strconv.Atoi(value)
	if err == nil {
		gate := os.NewFile(uintptr(fd), "launch gate")
		var b [1]byte
		_, err = io.ReadFull(gate, b[:])
		gate.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dyna: this process was not recorded for its session, so the engine would refuse it:", err)
		os.Exit(1)
	}
}

// Empty contains no committed launch records.
type Empty struct{}

func (Empty) ScopeIdentity(string) (Identity, bool, error)    { return Identity{}, false, nil }
func (Empty) ProcessIdentity(Process) (Identity, bool, error) { return Identity{}, false, nil }
