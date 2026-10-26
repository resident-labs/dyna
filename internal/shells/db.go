package shells

import (
	"context"
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
)

const schemaVersion = 5

const schema = `CREATE TABLE shell_jobs (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	cmd TEXT NOT NULL,
	cwd TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	keep INTEGER NOT NULL DEFAULT 0,
	room_id TEXT NOT NULL DEFAULT '',
	room TEXT NOT NULL DEFAULT '',
	pid INTEGER NOT NULL DEFAULT 0,
	supervisor INTEGER NOT NULL DEFAULT 0,
	supervisor_start INTEGER NOT NULL DEFAULT 0,
	started TEXT NOT NULL,
	ended TEXT,
	exit_code INTEGER NOT NULL DEFAULT 0,
	exit_signal TEXT NOT NULL DEFAULT '',
	exit_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX shell_jobs_open ON shell_jobs(started) WHERE ended IS NULL;`

// Version 2 tracks what the owning session has already seen: the output
// read cursor, a live foreground waiter, and whether the exit was delivered.
const schemaV2 = `ALTER TABLE shell_jobs ADD COLUMN read_offset INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shell_jobs ADD COLUMN waiter_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shell_jobs ADD COLUMN delivered INTEGER NOT NULL DEFAULT 0;`

// Version 3 records what the terminal view shows: when a foreground command
// moved to the background, who stopped it, and how many lines it wrote.
// Only the setters below write these columns, so save leaves them alone.
const schemaV3 = `ALTER TABLE shell_jobs ADD COLUMN background_at TEXT;
ALTER TABLE shell_jobs ADD COLUMN stop_by TEXT NOT NULL DEFAULT '';
ALTER TABLE shell_jobs ADD COLUMN output_lines INTEGER NOT NULL DEFAULT 0;`

// Version 5 names the workflow run whose worker started a job. Workflow
// workers inherit their parent session's identity, so the owner alone cannot
// attribute a job to the run that cancelling must stop (D-165).
const schemaV5 = `ALTER TABLE shell_jobs ADD COLUMN run TEXT NOT NULL DEFAULT '';`

const columns = `id, name, cmd, cwd, owner, keep, room_id, room, pid, supervisor, supervisor_start, started, ended, exit_code, exit_signal, exit_error, run`

// readColumns are the columns a read returns: columns, then the version 3 ones.
const readColumns = columns + `, background_at, stop_by, output_lines`

// Migrate brings the shell job tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := (&Store{Root: filepath.Join(dataDir, "shells")}).db()
	return err
}

// db opens dyna.db in the data folder that holds the shell log directories.
func (s *Store) db() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "shells.version", schemaVersion, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old == 0 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		if old == 0 || old == 1 {
			if _, err := conn.ExecContext(ctx, schemaV2); err != nil {
				return err
			}
		}
		if old < 3 {
			if _, err := conn.ExecContext(ctx, schemaV3); err != nil {
				return err
			}
		}
		if old < 5 {
			if _, err := conn.ExecContext(ctx, schemaV5); err != nil {
				return err
			}
		}
		// The legacy import writes current rows, so it needs every column.
		if old == 0 {
			if err := importLegacy(conn, s.Root); err != nil {
				return err
			}
		}
		return importLegacyExits(ctx, conn, s.Root)
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// saveNew writes a new job's metadata and, when hold is set, its exit hold in
// the same transaction.
func (s *Store) saveNew(m Meta, hold time.Time) error {
	if hold.IsZero() {
		return s.Save(m, nil)
	}
	if !ValidID(m.ID) {
		return fmt.Errorf("invalid shell id %q", m.ID)
	}
	if err := s.injected("save"); err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if err := upsert(conn, m, nil); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `UPDATE shell_jobs SET waiter_until=? WHERE id=?`, hold.UnixMilli(), m.ID)
		return err
	}); err != nil {
		return err
	}
	s.Notify(m.ID)
	return nil
}

// Save writes a job's metadata and, once it has ended, its exit.
func (s *Store) Save(m Meta, ex *Exit) error {
	if !ValidID(m.ID) {
		return fmt.Errorf("invalid shell id %q", m.ID)
	}
	if err := s.injected("save"); err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	// No topic: the room watch still scans shell jobs (item 16).
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return upsert(conn, m, ex) }); err != nil {
		return err
	}
	s.Notify(m.ID)
	return nil
}

// writeWait bounds a shell write's wait for the writer, so a busy engine
// never holds a supervisor or a command without limit.
const writeWait = 10 * time.Second

func upsert(conn *sql.Conn, m Meta, ex *Exit) error {
	cmd, err := json.Marshal(m.Cmd)
	if err != nil {
		return err
	}
	var ended any
	var code int
	var signal, message string
	if ex != nil {
		ended, code, signal, message = ex.Ended.UTC().Format(time.RFC3339Nano), ex.Code, ex.Signal, ex.Error
	}
	_, err = conn.ExecContext(context.Background(), `INSERT INTO shell_jobs(`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, cmd=excluded.cmd, cwd=excluded.cwd, owner=excluded.owner, keep=excluded.keep,
	run=excluded.run, room_id=excluded.room_id, room=excluded.room, pid=excluded.pid, supervisor=excluded.supervisor, supervisor_start=excluded.supervisor_start,
	started=excluded.started, ended=excluded.ended, exit_code=excluded.exit_code, exit_signal=excluded.exit_signal, exit_error=excluded.exit_error`,
		m.ID, m.Name, string(cmd), m.Cwd, m.Owner, m.Keep, m.RoomID, m.Room, m.Pid, m.Supervisor, int64(m.SupervisorStartTime),
		m.Started.UTC().Format(time.RFC3339Nano), ended, code, signal, message, m.Run)
	return err
}

// Infos returns the jobs in ids with their live status.
func (s *Store) Infos(ids []string) ([]Info, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	list, err := s.loadAll("WHERE id IN (?"+strings.Repeat(",?", len(ids)-1)+")", args...)
	for i := range list {
		list[i] = s.status(list[i])
	}
	return list, err
}

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (Meta, *Exit, error) {
	var m Meta
	var cmd, started string
	var ended, background sql.NullString
	var start int64
	var ex Exit
	if err := row.Scan(&m.ID, &m.Name, &cmd, &m.Cwd, &m.Owner, &m.Keep, &m.RoomID, &m.Room, &m.Pid, &m.Supervisor, &start, &started, &ended, &ex.Code, &ex.Signal, &ex.Error, &m.Run, &background, &m.StopBy, &m.OutputLines); err != nil {
		return Meta{}, nil, err
	}
	m.SupervisorStartTime = uint64(start)
	if err := json.Unmarshal([]byte(cmd), &m.Cmd); err != nil {
		return Meta{}, nil, fmt.Errorf("shell %s has an invalid command: %w", m.ID, err)
	}
	var err error
	if m.Started, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return Meta{}, nil, err
	}
	if background.Valid {
		at, err := time.Parse(time.RFC3339Nano, background.String)
		if err != nil {
			return Meta{}, nil, err
		}
		m.BackgroundAt = &at
	}
	if !ended.Valid {
		return m, nil, nil
	}
	if ex.Ended, err = time.Parse(time.RFC3339Nano, ended.String); err != nil {
		return Meta{}, nil, err
	}
	return m, &ex, nil
}

// load reads one job. A missing job wraps os.ErrNotExist.
func (s *Store) load(id string) (Meta, *Exit, error) {
	if !ValidID(id) {
		return Meta{}, nil, fmt.Errorf("invalid shell id %q", id)
	}
	db, err := s.db()
	if err != nil {
		return Meta{}, nil, err
	}
	m, ex, err := scanJob(db.QueryRow(`SELECT `+readColumns+` FROM shell_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, nil, fmt.Errorf("no such shell %q: %w", id, os.ErrNotExist)
	}
	return m, ex, err
}

// loadAll reads jobs newest first, filtered by where (a clause or empty).
func (s *Store) loadAll(where string, args ...any) ([]Info, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT `+readColumns+` FROM shell_jobs `+where+` ORDER BY started DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		m, ex, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Info{Meta: m, Exit: ex})
	}
	return out, rows.Err()
}

// TODO: legacy/shim remove later — imports shell jobs that older releases kept in <data>/shells/<id>/meta.json and exit.json; remove when no supported data folder predates dyna.db shell jobs.
func importLegacy(conn *sql.Conn, root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if errors.Is(err, os.ErrNotExist) {
			unpublished, err := legacyUnpublished(dir, e.Name())
			if err != nil {
				return err
			}
			if unpublished {
				continue
			}
			return fmt.Errorf("shell %s has job files but no meta.json; repair or remove %s", e.Name(), dir)
		}
		if err != nil {
			return fmt.Errorf("read shell %s: %w", e.Name(), err)
		}
		var m Meta
		if err := json.Unmarshal(raw, &m); err != nil || m.ID != e.Name() || len(m.Cmd) == 0 {
			return fmt.Errorf("shell %s has an invalid meta.json; repair or remove %s", e.Name(), dir)
		}
		ex, err := readLegacyExit(dir)
		if err != nil {
			return err
		}
		if err := upsert(conn, m, ex); err != nil {
			return err
		}
	}
	return nil
}

// legacyUnpublished reports whether a directory holds only the staging
// record of a job that never started: no files, or one meta.json.tmp with
// this ID and no process.
func legacyUnpublished(dir, id string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() != "meta.json.tmp" || !e.Type().IsRegular() {
			return false, nil
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		var m Meta
		if json.Unmarshal(raw, &m) != nil || m.ID != id || m.Pid != 0 || m.Supervisor != 0 {
			return false, nil
		}
	}
	return true, nil
}

// TODO: legacy/shim remove later — reads exits only during schema upgrade;
// remove when supported data directories have passed shells.version 4.
// Supervisors from before the SQLite cutover must be drained before upgrade.
func readLegacyExit(dir string) (*Exit, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "exit.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ex Exit
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, fmt.Errorf("shell %s has an invalid exit.json: %w", filepath.Base(dir), err)
	}
	return &ex, nil
}

// TODO: legacy/shim remove later — imports retained exits for existing jobs;
// remove when supported data directories have passed shells.version 4.
func importLegacyExits(ctx context.Context, conn *sql.Conn, root string) error {
	rows, err := conn.QueryContext(ctx, "SELECT id FROM shell_jobs WHERE ended IS NULL")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !ValidID(id) {
			return fmt.Errorf("invalid shell id %q", id)
		}
		ex, err := readLegacyExit(filepath.Join(root, id))
		if err != nil {
			return err
		}
		if ex == nil {
			continue
		}
		// Only exit columns change; canonical metadata and delivery fields survive.
		if _, err := conn.ExecContext(ctx, `UPDATE shell_jobs SET ended=?,exit_code=?,exit_signal=?,exit_error=? WHERE id=? AND ended IS NULL`,
			ex.Ended.UTC().Format(time.RFC3339Nano), ex.Code, ex.Signal, ex.Error, id); err != nil {
			return err
		}
	}
	return nil
}

// exec runs one statement against the row of job id and reports whether it
// matched.
func (s *Store) exec(id, query string, args ...any) (bool, error) {
	if err := s.injected(query); err != nil {
		return false, err
	}
	db, err := s.db()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	var n int64
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, query, args...)
		if err == nil {
			n, err = res.RowsAffected()
		}
		return err
	})
	if n > 0 && err == nil {
		s.Notify(id)
	}
	return n > 0, err
}

// ClaimExit marks a finished job's exit as seen by its owner. Exactly one
// caller gets true, so the owner learns of each exit once.
func (s *Store) ClaimExit(id string) (bool, error) {
	return s.exec(id, `UPDATE shell_jobs SET delivered=1 WHERE id=? AND delivered=0 AND ended IS NOT NULL`, id)
}

// HoldExit tells the supervisor that a foreground caller waits for this job
// until the given time, so it does not also wake the owner.
func (s *Store) HoldExit(id string, until time.Time) error {
	_, err := s.exec(id, `UPDATE shell_jobs SET waiter_until=? WHERE id=?`, until.UnixMilli(), id)
	return err
}

func (s *Store) hold(id string) (time.Time, bool, error) {
	db, err := s.db()
	if err != nil {
		return time.Time{}, false, err
	}
	var ms int64
	var delivered bool
	err = db.QueryRow(`SELECT waiter_until, delivered FROM shell_jobs WHERE id=?`, id).Scan(&ms, &delivered)
	return time.UnixMilli(ms), delivered, err
}

// ReadOffset is how far the owner has read the job's output.
func (s *Store) ReadOffset(id string) (int64, error) {
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	var off int64
	err = db.QueryRow(`SELECT read_offset FROM shell_jobs WHERE id=?`, id).Scan(&off)
	return off, err
}

// SetReadOffset records how far the owner has read the job's output.
func (s *Store) SetReadOffset(id string, off int64) error {
	_, err := s.exec(id, `UPDATE shell_jobs SET read_offset=? WHERE id=?`, off, id)
	return err
}

// MarkBackground records that a foreground command outlived its limit.
func (s *Store) MarkBackground(id string, at time.Time) error {
	_, err := s.exec(id, `UPDATE shell_jobs SET background_at=? WHERE id=?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

// markStopped records who stopped the job. The first stop wins.
func (s *Store) markStopped(id, by string) error {
	_, err := s.exec(id, `UPDATE shell_jobs SET stop_by=? WHERE id=? AND stop_by=''`, by, id)
	return err
}

// setOutputLines records how many lines a finished job wrote.
func (s *Store) setOutputLines(id string, n int) error {
	_, err := s.exec(id, `UPDATE shell_jobs SET output_lines=? WHERE id=?`, n, id)
	return err
}
