package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// State is the life of one job, from its submission to its recorded end.
type State string

// Job states. The spelling is stored, so it must not change.
const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StatePassed    State = "passed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateLost      State = "lost"
	StateTimedOut  State = "timed_out"
)

// Finished reports whether no later event changes the job's state.
func (s State) Finished() bool {
	switch s {
	case StatePassed, StateFailed, StateCancelled, StateLost, StateTimedOut:
		return true
	}
	return false
}

// Label is how a state reads to a person.
func (s State) Label() string {
	switch s {
	case StateQueued:
		return "queued"
	case StateRunning:
		return "running"
	case StatePassed:
		return "passed"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	case StateLost:
		return "lost"
	case StateTimedOut:
		return "timed out"
	}
	return ""
}

// Job is one command that runs on a node. A saved row in SQLite stays the
// authority for the job's state; the log bytes live in a file.
type Job struct {
	ID string
	// RoomID is the room the job was submitted from, and Session the session
	// that submitted it. The job's events wake that session.
	RoomID  string
	Session string
	// Run is the workflow run that submitted the job, when one did.
	Run string
	// IdempotencyKey is stable across a workflow resume chain. Nonempty keys
	// are unique, so an uncertain submission recovers this row.
	IdempotencyKey string
	// RequestHash verifies that a repeated key names the same request.
	RequestHash string
	// Node is the registered machine name. Empty means the local node.
	Node    string
	Command []string
	// Repo and SHA name the exact commit the job checks out on the node. Both
	// empty means the job runs with no source.
	Repo string
	SHA  string
	// Host runs the command on the node itself; otherwise Image names the
	// container the command runs in.
	Host      bool
	Image     string
	Container ContainerOptions
	// CPUs and MemoryMiB are what the submitter asked for, before the node
	// clamped them. Timeout bounds the job; zero uses the default. Idle stops
	// a running job after that long without output; zero disables it. Heavy
	// marks an IO-heavy job, which a node runs only a few of.
	CPUs      int
	MemoryMiB int64
	Heavy     bool
	// Resources are named units the job holds for its whole run.
	Resources []machines.ResourceName
	Timeout   time.Duration
	Idle      time.Duration
	State     State
	// ExitCode is the command's status; nil while the job has not ended.
	ExitCode *int
	// LogPath is the local file the job's output streams to.
	LogPath string
	Created time.Time
	Started time.Time
	Ended   time.Time
	// Dir and Unit locate the job on its node, so a restarted engine
	// reattaches to it. LogBytes is how much of its log the engine already
	// stored.
	Dir      string
	Unit     string
	LogBytes int64
	// Note says what Dyna clamped about the request.
	Note string
	// WaitReason is why a queued job has not started: what the node it waits
	// for is short of, and its place in that node's queue.
	WaitReason string
	// CancelRequested records a standing cancellation: the engine that
	// holds the executor, or the next one after a restart, stops the process
	// and records the end.
	CancelRequested bool
	// StartPosted records that the job's start event went out. A restart at
	// the boundary - the running row saved, the event not yet posted - reads
	// it and posts the start before the job runs on.
	StartPosted bool
	// Launched records that the node accepted the unit: StartDetached
	// returned. A running row without it - a crash or a node-side death
	// inside the start, after the folder was made but before the unit ran -
	// relaunches on the next engine instead of attaching to a unit that
	// never was.
	Launched bool
	// Artifacts are paths in the job's work folder that the engine copies
	// back when the job ends, before the folder is removed.
	Artifacts []string
	// Workspace is the kept checkout on the node. Empty gives the job a fresh
	// work folder under Dir.
	Workspace string
}

// WaitResult is one bounded WaitJob call. Done is false when its event wait
// ended before the job finished; the caller starts another event wait.
type WaitResult struct {
	Job  Job
	Done bool
}

// Topic is the hub topic a jobs-table write publishes under.
const Topic = "jobs"

const schemaVersion = 10

const schema = `CREATE TABLE jobs (
	id TEXT PRIMARY KEY,
	room_id TEXT NOT NULL DEFAULT '',
	session TEXT NOT NULL DEFAULT '',
	run TEXT NOT NULL DEFAULT '',
	idempotency_key TEXT NOT NULL DEFAULT '',
	request_hash TEXT NOT NULL DEFAULT '',
	node TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '[]',
	repo TEXT NOT NULL DEFAULT '',
	sha TEXT NOT NULL DEFAULT '',
	host INTEGER NOT NULL DEFAULT 0,
	image TEXT NOT NULL DEFAULT '',
	cpus INTEGER NOT NULL DEFAULT 0,
	memory_mib INTEGER NOT NULL DEFAULT 0,
	heavy INTEGER NOT NULL DEFAULT 0,
	timeout_s INTEGER NOT NULL DEFAULT 0,
	idle_s INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL,
	exit_code INTEGER,
	log_path TEXT NOT NULL DEFAULT '',
	created_ms INTEGER NOT NULL,
	started_ms INTEGER NOT NULL DEFAULT 0,
	ended_ms INTEGER NOT NULL DEFAULT 0,
	dir TEXT NOT NULL DEFAULT '',
	unit TEXT NOT NULL DEFAULT '',
	log_bytes INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	wait_reason TEXT NOT NULL DEFAULT '',
	cancel_requested INTEGER NOT NULL DEFAULT 0,
	start_posted INTEGER NOT NULL DEFAULT 0,
	launched INTEGER NOT NULL DEFAULT 0,
	artifacts TEXT NOT NULL DEFAULT '[]',
	resources TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(resources)),
	workspace TEXT NOT NULL DEFAULT '',
	container TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(container))
);
CREATE INDEX jobs_open ON jobs(created_ms) WHERE state IN ('queued','running');
CREATE UNIQUE INDEX jobs_idempotency ON jobs(idempotency_key) WHERE idempotency_key <> '';` + workspaceSchema

// Store reads and writes the jobs table of dataDir's dyna.db.
type Store struct {
	// DataDir is the engine's data folder. Empty uses the default.
	DataDir string
	// clock reads the time a row records. Tests replace it.
	clock func() time.Time
}

// Migrate brings the jobs table in dataDir's dyna.db to this version. Only the
// engine calls it, at start.
func Migrate(dataDir string) error {
	_, err := (&Store{DataDir: dataDir}).db()
	return err
}

func (s Store) db() (*sql.DB, error) {
	dir := s.DataDir
	if dir == "" {
		dir = defaultDataDir()
	}
	db, err := database.Open(dir)
	if err != nil {
		return nil, err
	}
	apply := func(conn *sql.Conn, old int) error {
		if old == 0 {
			if _, err := conn.ExecContext(context.Background(), schema); err != nil {
				return err
			}
		}
		if old > 0 && old < 2 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN artifacts TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return err
			}
		}
		if old > 0 && old < 4 {
			// A job written before named resources requested none.
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN resources TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(resources))"); err != nil {
				return err
			}
		}
		if old > 0 && old < 3 {
			// A job written before the node queue has neither its IO weight
			// nor a wait reason; both read as none.
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN heavy INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN wait_reason TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if old > 0 && old < 5 {
			// Jobs written before idle timeouts have none.
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN idle_s INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
		if old > 0 && old < 6 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN workspace TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if _, err := conn.ExecContext(context.Background(), workspaceSchema); err != nil {
				return err
			}
		}
		if old > 0 && old < 7 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN run TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if old > 0 && old < 8 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if _, err := conn.ExecContext(context.Background(), "CREATE UNIQUE INDEX jobs_idempotency ON jobs(idempotency_key) WHERE idempotency_key <> ''"); err != nil {
				return err
			}
		}
		if old > 0 && old < 9 {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN request_hash TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if old > 0 && old < 10 {
			// Jobs written before container access options used Docker defaults.
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE jobs ADD COLUMN container TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(container))"); err != nil {
				return err
			}
		}
		return nil
	}
	return db, database.Migrate(db, "jobs.version", schemaVersion, apply)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s Store) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// defaultDataDir is the engine's data folder. Tests replace it with DataDir.
var defaultDataDir = store.DataDir

// NewID returns an unused job ID.
func (s Store) NewID() (string, error) {
	db, err := s.db()
	if err != nil {
		return "", err
	}
	var raw [6]byte
	for attempt := 0; attempt < 10; attempt++ {
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		id := fmt.Sprintf("job_%x", raw)
		var used int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM jobs WHERE id=?", id).Scan(&used); err != nil {
			return "", err
		}
		if used == 0 {
			return id, nil
		}
	}
	return "", errors.New("no unused job id")
}

// Save inserts or updates one job row. The write publishes on the jobs topic,
// so the engine's job worker sees a queued job at once.
func (s Store) Save(ctx context.Context, job Job) error {
	_, err := s.save(ctx, job, false)
	return err
}

// save writes one job. A submission also joins its kept workspace in the same
// transaction, so cleanup cannot take the folder between those two writes.
func (s Store) save(ctx context.Context, job Job, submit bool) (Job, error) {
	db, err := s.db()
	if err != nil {
		return job, err
	}
	// The command is a JSON array, so every byte of a word round-trips.
	encoded, err := json.Marshal(job.Command)
	if err != nil {
		return job, err
	}
	var exit any
	if job.ExitCode != nil {
		exit = *job.ExitCode
	}
	ms := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixMilli()
	}
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		if submit && job.Workspace != "" {
			kept, err := ensureWorkspace(ctx, conn, job, s.now())
			if err != nil {
				return err
			}
			job.Workspace = kept
		}
		return upsertJob(ctx, conn, job, encoded, exit, ms)
	})
	return job, err
}

// SaveOpen writes job the way Save does, inside one transaction that reads
// the row first: a row that already reached a terminal state keeps it and
// wins, so a late cancel is never overwritten by a concurrent start or
// finish. It returns the row that holds after the call and whether job's own
// write went through.
func (s Store) SaveOpen(ctx context.Context, job Job) (Job, bool, error) {
	db, err := s.db()
	if err != nil {
		return job, false, err
	}
	encoded, err := json.Marshal(job.Command)
	if err != nil {
		return job, false, err
	}
	var exit any
	if job.ExitCode != nil {
		exit = *job.ExitCode
	}
	ms := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixMilli()
	}
	winner, won := job, false
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		row := conn.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id=?", job.ID)
		current, err := scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			won = true // the row is new; the write below creates it
			return upsertJob(ctx, conn, job, encoded, exit, ms)
		}
		if err != nil {
			return err
		}
		if current.State.Finished() {
			winner = current
			return nil
		}
		won = true
		if err := upsertJob(ctx, conn, job, encoded, exit, ms); err != nil {
			return err
		}
		if job.State.Finished() && job.Workspace != "" {
			return touchWorkspace(ctx, conn, job.Workspace, s.now())
		}
		return nil
	})
	return winner, won, err
}

// MarkLaunched records that the node accepted the unit, as soon as the
// start call returns. It is one conditional column write, not a whole-row
// save, so it can never race a poll's stale copy. A row that ends while
// the start call was in flight keeps its own end and is not marked.
func (s Store) MarkLaunched(ctx context.Context, id string) {
	db, err := s.db()
	if err != nil {
		return
	}
	_ = state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE jobs SET launched=1 WHERE id=? AND state='running'", id)
		return err
	})
}

// RequestCancel records one durable cancellation request on the row. It
// never ends the row and never reports: the engine that owns the job - a
// queued one on the next tick of any engine, a running one on the next tick
// of the engine that holds its executor - carries the request to the
// recorded end. A missing job reports not-found; a job that already ended
// keeps its own end.
func (s Store) RequestCancel(ctx context.Context, id string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	taken := false
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, "UPDATE jobs SET cancel_requested=1 WHERE id=? AND state IN ('queued','running')", id)
		if err != nil {
			return err
		}
		taken = true
		_, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return err
	}
	if !taken {
		return nil
	}
	// The row may have already ended, or carry the request already; only a
	// missing job is an error.
	if row, err := scanJob(db.QueryRowContext(ctx, "SELECT state FROM jobs WHERE id=?", id)); errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	} else if err == nil && row.State.Finished() {
		return os.ErrNotExist
	}
	return nil
}

// EndQueuedCancels ends every queued job that carries a cancellation
// request, and returns the rows this call ended. Each end is one conditional
// write, so when two engines sweep at once, only the one whose write took
// the row reports it.
func (s Store) EndQueuedCancels(ctx context.Context, ended time.Time) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var stopped []Job
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='queued' AND cancel_requested=1 ORDER BY created_ms")
		if err != nil {
			return err
		}
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				rows.Close()
				return err
			}
			job.State = StateCancelled
			job.Ended = ended
			encoded, err := json.Marshal(job.Command)
			if err != nil {
				rows.Close()
				return err
			}
			var exit any
			if job.ExitCode != nil {
				exit = *job.ExitCode
			}
			ms := func(t time.Time) int64 {
				if t.IsZero() {
					return 0
				}
				return t.UnixMilli()
			}
			if err := upsertJob(ctx, conn, job, encoded, exit, ms); err != nil {
				rows.Close()
				return err
			}
			stopped = append(stopped, job)
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	return stopped, err
}

// RequestedRunning returns every running job that carries a cancellation
// request. The engine that holds the job's executor stops it on its next
// tick; a restarted engine reattaches the row first.
func (s Store) RequestedRunning(ctx context.Context) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='running' AND cancel_requested=1")
	if err != nil {
		return nil, err
	}
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, job)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// upsertJob writes the whole row of one job.
func upsertJob(ctx context.Context, conn *sql.Conn, job Job, encoded []byte, exit any, ms func(time.Time) int64) error {
	command := string(encoded)
	artifacts, err := json.Marshal(job.Artifacts)
	if err != nil {
		return err
	}
	resources, err := json.Marshal(job.Resources)
	if err != nil {
		return err
	}
	container, err := json.Marshal(job.Container)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO jobs(id,room_id,session,run,idempotency_key,request_hash,node,command,repo,sha,host,image,cpus,memory_mib,heavy,timeout_s,idle_s,state,exit_code,log_path,created_ms,started_ms,ended_ms,dir,unit,log_bytes,note,wait_reason,cancel_requested,start_posted,launched,artifacts,resources,workspace,container)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET room_id=excluded.room_id, session=excluded.session, run=excluded.run, idempotency_key=excluded.idempotency_key, request_hash=excluded.request_hash, node=excluded.node, command=excluded.command, repo=excluded.repo, sha=excluded.sha, host=excluded.host, image=excluded.image, cpus=excluded.cpus, memory_mib=excluded.memory_mib, heavy=excluded.heavy, timeout_s=excluded.timeout_s, idle_s=excluded.idle_s, state=excluded.state, exit_code=excluded.exit_code, log_path=excluded.log_path, started_ms=excluded.started_ms, ended_ms=excluded.ended_ms, dir=excluded.dir, unit=excluded.unit, log_bytes=excluded.log_bytes, note=excluded.note, wait_reason=excluded.wait_reason, cancel_requested=jobs.cancel_requested|excluded.cancel_requested, start_posted=jobs.start_posted|excluded.start_posted, launched=jobs.launched|excluded.launched, artifacts=excluded.artifacts, resources=excluded.resources, workspace=excluded.workspace, container=excluded.container`,
		job.ID, job.RoomID, job.Session, job.Run, job.IdempotencyKey, job.RequestHash, job.Node, command, job.Repo, job.SHA, job.Host, job.Image, job.CPUs, job.MemoryMiB, boolInt(job.Heavy), int64(job.Timeout/time.Second), int64(job.Idle/time.Second), job.State, exit, job.LogPath, ms(job.Created), ms(job.Started), ms(job.Ended), job.Dir, job.Unit, job.LogBytes, job.Note, job.WaitReason, boolInt(job.CancelRequested), boolInt(job.StartPosted), boolInt(job.Launched), string(artifacts), string(resources), job.Workspace, string(container))
	return err
}

const columns = "id,room_id,session,run,idempotency_key,request_hash,node,command,repo,sha,host,image,cpus,memory_mib,heavy,timeout_s,idle_s,state,exit_code,log_path,created_ms,started_ms,ended_ms,dir,unit,log_bytes,note,wait_reason,cancel_requested,start_posted,launched,artifacts,resources,workspace,container"

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var command, artifacts, resources, container string
	var exit sql.NullInt64
	var requested, posted, launched, heavy int
	var host, timeout, idle int64
	var created, started, ended int64
	if err := row.Scan(&job.ID, &job.RoomID, &job.Session, &job.Run, &job.IdempotencyKey, &job.RequestHash, &job.Node, &command, &job.Repo, &job.SHA, &host, &job.Image, &job.CPUs, &job.MemoryMiB, &heavy, &timeout, &idle, &job.State, &exit, &job.LogPath, &created, &started, &ended, &job.Dir, &job.Unit, &job.LogBytes, &job.Note, &job.WaitReason, &requested, &posted, &launched, &artifacts, &resources, &job.Workspace, &container); err != nil {
		return job, err
	}
	job.Timeout = time.Duration(timeout) * time.Second
	job.Idle = time.Duration(idle) * time.Second
	job.Host = host == 1
	job.Heavy = heavy == 1
	job.CancelRequested = requested == 1
	job.StartPosted = posted == 1
	job.Launched = launched == 1
	if exit.Valid {
		code := int(exit.Int64)
		job.ExitCode = &code
	}
	job.Created = time.UnixMilli(created)
	if started > 0 {
		job.Started = time.UnixMilli(started)
	}
	if ended > 0 {
		job.Ended = time.UnixMilli(ended)
	}
	words, err := unquoteCommand(command)
	if err != nil {
		return job, err
	}
	job.Command = words
	if err := json.Unmarshal([]byte(artifacts), &job.Artifacts); err != nil {
		return job, fmt.Errorf("job artifacts %q: %w", artifacts, err)
	}
	if err := json.Unmarshal([]byte(resources), &job.Resources); err != nil {
		return job, fmt.Errorf("job resources %q: %w", resources, err)
	}
	if err := json.Unmarshal([]byte(container), &job.Container); err != nil {
		return job, fmt.Errorf("job container options %q: %w", container, err)
	}
	return job, nil
}

// unquoteCommand reads the stored JSON array of command words.
func unquoteCommand(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var words []string
	if err := json.Unmarshal([]byte(raw), &words); err != nil {
		return nil, fmt.Errorf("job command %q: %w", raw, err)
	}
	return words, nil
}

// Get returns one job by its ID.
func (s Store) Get(ctx context.Context, id string) (Job, error) {
	db, err := s.db()
	if err != nil {
		return Job{}, err
	}
	row := db.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE id=?", id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, os.ErrNotExist
	}
	return job, err
}

// GetByIdempotencyKey returns the original job of a workflow invocation.
func (s Store) GetByIdempotencyKey(ctx context.Context, key string) (Job, error) {
	db, err := s.db()
	if err != nil {
		return Job{}, err
	}
	row := db.QueryRowContext(ctx, "SELECT "+columns+" FROM jobs WHERE idempotency_key=?", key)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, os.ErrNotExist
	}
	return job, err
}

// List returns the newest jobs first. Unfinished only, when open is set.
func (s Store) List(ctx context.Context, open bool) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + columns + " FROM jobs"
	if open {
		query += " WHERE state IN ('queued','running')"
	}
	query += " ORDER BY created_ms DESC LIMIT 200"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, job)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// ClaimQueued returns every queued job, oldest first, whose ID is in ids when
// ids is not nil. The caller starts them.
func (s Store) ClaimQueued(ctx context.Context) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='queued' AND cancel_requested=0 ORDER BY created_ms")
	if err != nil {
		return nil, err
	}
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, job)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// Running returns every job the engine may need to reattach to.
func (s Store) Running(ctx context.Context) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='running' ORDER BY created_ms")
	if err != nil {
		return nil, err
	}
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, job)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// NodeOpen returns one node's open jobs, oldest first, with no list limit: a
// node's queue must count every unit it holds, even behind a backlog of newer
// work on other nodes.
func (s Store) NodeOpen(ctx context.Context, node string) ([]Job, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE node=? AND state IN ('queued','running') ORDER BY created_ms, id", node)
	if err != nil {
		return nil, err
	}
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, job)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// LogDir is the folder a job's local log file lives in.
func LogDir(dataDir, id string) string {
	return filepath.Join(dataDir, "jobs", id)
}
