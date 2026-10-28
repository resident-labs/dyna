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
	// Node is the registered machine name. Empty means the local node.
	Node    string
	Command []string
	// Repo and SHA name the exact commit the job checks out on the node. Both
	// empty means the job runs with no source.
	Repo string
	SHA  string
	// Host runs the command on the node itself; otherwise Image names the
	// container the command runs in.
	Host  bool
	Image string
	// CPUs and MemoryMiB are what the submitter asked for, before the node
	// clamped them. Timeout bounds the job; zero uses the default.
	CPUs      int
	MemoryMiB int64
	Timeout   time.Duration
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
}

// Topic is the hub topic a jobs-table write publishes under.
const Topic = "jobs"

const schemaVersion = 1

const schema = `CREATE TABLE jobs (
	id TEXT PRIMARY KEY,
	room_id TEXT NOT NULL DEFAULT '',
	session TEXT NOT NULL DEFAULT '',
	node TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '[]',
	repo TEXT NOT NULL DEFAULT '',
	sha TEXT NOT NULL DEFAULT '',
	host INTEGER NOT NULL DEFAULT 0,
	image TEXT NOT NULL DEFAULT '',
	cpus INTEGER NOT NULL DEFAULT 0,
	memory_mib INTEGER NOT NULL DEFAULT 0,
	timeout_s INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL,
	exit_code INTEGER,
	log_path TEXT NOT NULL DEFAULT '',
	created_ms INTEGER NOT NULL,
	started_ms INTEGER NOT NULL DEFAULT 0,
	ended_ms INTEGER NOT NULL DEFAULT 0,
	dir TEXT NOT NULL DEFAULT '',
	unit TEXT NOT NULL DEFAULT '',
	log_bytes INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX jobs_open ON jobs(created_ms) WHERE state IN ('queued','running');`

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
			_, err := conn.ExecContext(context.Background(), schema)
			return err
		}
		return nil
	}
	return db, database.Migrate(db, "jobs.version", schemaVersion, apply)
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
	db, err := s.db()
	if err != nil {
		return err
	}
	// The command is a JSON array, so every byte of a word round-trips.
	encoded, err := json.Marshal(job.Command)
	if err != nil {
		return err
	}
	command := string(encoded)
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
	return state.For(db).Write(ctx, hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO jobs(id,room_id,session,node,command,repo,sha,host,image,cpus,memory_mib,timeout_s,state,exit_code,log_path,created_ms,started_ms,ended_ms,dir,unit,log_bytes,note)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET room_id=excluded.room_id, session=excluded.session, node=excluded.node, command=excluded.command, repo=excluded.repo, sha=excluded.sha, host=excluded.host, image=excluded.image, cpus=excluded.cpus, memory_mib=excluded.memory_mib, timeout_s=excluded.timeout_s, state=excluded.state, exit_code=excluded.exit_code, log_path=excluded.log_path, started_ms=excluded.started_ms, ended_ms=excluded.ended_ms, dir=excluded.dir, unit=excluded.unit, log_bytes=excluded.log_bytes, note=excluded.note`,
			job.ID, job.RoomID, job.Session, job.Node, command, job.Repo, job.SHA, job.Host, job.Image, job.CPUs, job.MemoryMiB, int64(job.Timeout/time.Second), job.State, exit, job.LogPath, ms(job.Created), ms(job.Started), ms(job.Ended), job.Dir, job.Unit, job.LogBytes, job.Note)
		return err
	})
}

const columns = "id,room_id,session,node,command,repo,sha,host,image,cpus,memory_mib,timeout_s,state,exit_code,log_path,created_ms,started_ms,ended_ms,dir,unit,log_bytes,note"

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var command string
	var exit sql.NullInt64
	var host, timeout int64
	var created, started, ended int64
	if err := row.Scan(&job.ID, &job.RoomID, &job.Session, &job.Node, &command, &job.Repo, &job.SHA, &host, &job.Image, &job.CPUs, &job.MemoryMiB, &timeout, &job.State, &exit, &job.LogPath, &created, &started, &ended, &job.Dir, &job.Unit, &job.LogBytes, &job.Note); err != nil {
		return job, err
	}
	job.Timeout = time.Duration(timeout) * time.Second
	job.Host = host == 1
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
	rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM jobs WHERE state='queued' ORDER BY created_ms")
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

// LogDir is the folder a job's local log file lives in.
func LogDir(dataDir, id string) string {
	return filepath.Join(dataDir, "jobs", id)
}
