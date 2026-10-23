package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/workeroutput/projection"
)

// runsSchema holds each run's meta (rule r10). body is the meta's JSON.
const runsSchema = `CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY,
 session TEXT NOT NULL,
 started INTEGER NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body))
);
CREATE INDEX IF NOT EXISTS runs_session ON runs(session, started);`

// runsRevSchema is version 2. Every write to a row stores the next table
// revision from runs_rev, which never goes back, so a reader asks for the
// rows written after the last revision it saw. status is the body's, for
// the running runs a reader checks. Existing rows start at revision 1.
const runsRevSchema = `CREATE TABLE runs_rev (n INTEGER NOT NULL);
INSERT INTO runs_rev VALUES (1);
ALTER TABLE runs ADD COLUMN rev INTEGER NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN status TEXT GENERATED ALWAYS AS (json_extract(body,'$.status')) VIRTUAL;
CREATE INDEX runs_by_rev ON runs(rev);
CREATE INDEX runs_by_status ON runs(status);`

// Migrate creates the runs table. Only the engine calls it, at start
// (requirement 41). Retained metadata is imported before normal readers.
func Migrate(dataDir string) error {
	if _, err := openRuns(dataDir); err != nil {
		return err
	}
	if err := projection.Migrate(dataDir); err != nil {
		return err
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return migrateOutput(db, dataDir)
}

// openRuns opens the runs table and the legacy import stamp table. Both
// migrate only in the engine and in tests; elsewhere an unmigrated store
// fails with its name (item 41).
func openRuns(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := backUpRunImport(db); err != nil {
		return db, err
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		return db, err
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		return db, err
	}
	err = database.Migrate(db, "runs.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, runsSchema); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, runsRevSchema)
		return err
	})
	if err != nil {
		return db, err
	}
	if err := migrateMeta(db, dataDir); err != nil {
		return db, err
	}
	if err := migrateSteering(db, dataDir); err != nil {
		return db, err
	}
	return db, migrateDirectories(db, dataDir)
}

// nextRev advances the runs table's revision and returns it.
func nextRev(ctx context.Context, conn *sql.Conn) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, "UPDATE runs_rev SET n=n+1 RETURNING n").Scan(&n)
	return n, err
}

func runsDB() (*sql.DB, error) { return openRuns(store.DataDir()) }

// metaKey names a retained run metadata file in the import stamp table.
func metaKey(id string) string { return "runs/" + id + "/meta" }

// lockMeta serializes run metadata writes across processes. The lock file
// stays outside the run folder so archived runs need not be writable.
func lockMeta(id string) (func(), error) {
	lockTaken()
	if err := os.MkdirAll(runLocksDir(), 0o700); err != nil {
		return nil, err
	}
	return store.Lock(metaLockPath(id))
}

func runLocksDir() string           { return filepath.Join(filepath.Dir(RunsDir()), "run-locks") }
func metaLockPath(id string) string { return filepath.Join(runLocksDir(), id+".lock") }

// removeRunLocks removes a deleted run's advisory locks.
func removeRunLocks(id string) error {
	for _, path := range []string{metaLockPath(id), ancestry.LockPath(store.DataDir(), id)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// lockTaken runs each time a run's lock is taken. Tests count it.
var lockTaken = func() {}

// changeMeta updates the canonical row.
func changeMeta(dir, id string, fn func(ctx context.Context, conn *sql.Conn) error) (Meta, error) {
	db, err := runsDB()
	if err != nil {
		return Meta{}, err
	}
	unlock, err := lockMeta(id)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()
	ctx := context.Background()
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return fn(ctx, conn) }); err != nil {
		return Meta{}, err
	}
	m, ok, err := storedMeta(db, id)
	if err == nil && !ok {
		err = fmt.Errorf("run %s has no meta", id)
	}
	return m, err
}

// TODO: legacy/shim remove later — reads a meta.json that an earlier release wrote, which reads and writes only the file; remove with item 31, when no run or command from before this release is alive.
func importMeta(id string) func(*sql.Conn, []byte) error {
	return func(conn *sql.Conn, raw []byte) error {
		// Decoding into Meta checks every field, so a file a reader could
		// not decode never becomes a row. The row stores the file as it is:
		// encoding a large Args again is slow for the biggest runs.
		var m Meta
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("run %s: meta.json: %v: %w", id, err, state.ErrSkipForeign)
		}
		if m.ID != id {
			return fmt.Errorf("run %s: meta.json names run %q: %w", id, m.ID, state.ErrSkipForeign)
		}
		var body bytes.Buffer
		if err := json.Compact(&body, raw); err != nil {
			return err
		}
		return putMeta(context.Background(), conn, m.ID, m.Session, m.StartedAt, body.String())
	}
}

// upsertMeta stores m as its run's row.
func upsertMeta(ctx context.Context, conn *sql.Conn, m Meta) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return putMeta(ctx, conn, m.ID, m.Session, m.StartedAt, string(body))
}

func putMeta(ctx context.Context, conn *sql.Conn, id, session string, started time.Time, body string) error {
	rev, err := nextRev(ctx, conn)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO runs(id,session,started,body,rev) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET session=excluded.session, started=excluded.started, body=excluded.body, rev=excluded.rev`,
		id, session, started.UnixNano(), body, rev)
	return err
}

// storedMeta is a run's row, or ok=false when it has none.
func storedMeta(db *sql.DB, id string) (m Meta, ok bool, err error) {
	m, err = LoadStored(context.Background(), db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// LoadStored reads the canonical run in the caller's database snapshot.
// It performs no file import; a missing row returns sql.ErrNoRows.
func LoadStored(ctx context.Context, reader state.Reader, id string) (Meta, error) {
	var m Meta
	var body string
	err := reader.QueryRowContext(ctx, "SELECT body FROM runs WHERE id=?", id).Scan(&body)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal([]byte(body), &m)
}

// metaAt reads a run's canonical row. Missing rows stay missing.
func metaAt(db *sql.DB, dir, id string) (Meta, error) {
	m, ok, err := storedMeta(db, id)
	if err == nil && !ok {
		err = &os.PathError{Op: "read", Path: filepath.Join(dir, "meta.json"), Err: os.ErrNotExist}
	}
	return m, err
}

// runFolders lists retained run folders without importing state during reads.
func runFolders(db *sql.DB, root string) ([]string, error) {
	list, err := cachedFolders(db, root)
	return slices.Clone(list.ids), err
}

// folders caches each root's run folders with root's modification time
// when they were read. Adding, removing or renaming a run folder changes
// that time, in any process.
var folders struct {
	sync.Mutex
	byRoot map[string]folderList
}

type folderList struct {
	modified time.Time
	racy     bool // read too close to modified to trust; walk again
	ids      []string
	rowless  []string // folders that had no row when read
}

// racyMargin: the kernel sets a folder's modification time from a coarse
// clock, so a run folder made in the same tick as a walk, just after it,
// leaves the time unchanged. A walk that ends this close to the time is
// used once and not trusted, as git does for racy files.
const racyMargin = time.Second

// walked runs once per walk over root. Tests count it.
var walked = func() {}

// cachedFolders is root's run folders, walked again only when root's
// modification time moved, or when the last walk was racy: one stat per
// call instead of a read of every entry.
func cachedFolders(db *sql.DB, root string) (folderList, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return folderList{}, nil
	}
	if err != nil {
		return folderList{}, err
	}
	folders.Lock()
	defer folders.Unlock()
	if f, ok := folders.byRoot[root]; ok && !f.racy && f.modified.Equal(info.ModTime()) {
		return f, nil
	}
	walked()
	entries, err := os.ReadDir(root)
	if err != nil {
		return folderList{}, err
	}
	known, err := queryIDs(db, "SELECT id FROM runs")
	if err != nil {
		return folderList{}, err
	}
	hasRow := make(map[string]bool, len(known))
	for _, id := range known {
		hasRow[id] = true
	}
	f := folderList{modified: info.ModTime()}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || validateRunID(id) != nil {
			continue
		}
		f.ids = append(f.ids, id)
		if !hasRow[id] {
			f.rowless = append(f.rowless, id)
		}
	}
	f.racy = time.Since(f.modified) < racyMargin
	if folders.byRoot == nil {
		folders.byRoot = map[string]folderList{}
	}
	folders.byRoot[root] = f
	return f, nil
}

func queryIDs(db *sql.DB, query string) ([]string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// metasWhere is the rows that match where, by id, and the highest revision
// among them. Writes take the next revision and commit in that order, so a
// reader that asks for rows past the highest one it saw misses none.
func metasWhere(db *sql.DB, where string, args ...any) (map[string]Meta, int64, error) {
	rows, err := db.Query("SELECT id, rev, body FROM runs WHERE "+where, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, top := map[string]Meta{}, int64(0)
	for rows.Next() {
		var id, body string
		var rev int64
		if err := rows.Scan(&id, &rev, &body); err != nil {
			return nil, 0, err
		}
		top = max(top, rev)
		// TODO: legacy/shim remove later — skips a row that an import stored before importMeta checked every field, so one bad field does not fail every list; remove with item 31, with the import.
		var m Meta
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			log.Printf("run %s: meta in dyna.db: %v", id, err)
			continue
		}
		out[id] = m
	}
	return out, top, rows.Err()
}

// metasIn is the rows of ids that match where. The table also holds the
// rows of archived runs, so a list reads only its own folders' rows.
func metasIn(db *sql.DB, ids []string, where string) (map[string]Meta, error) {
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, _, err := metasWhere(db, "id IN (SELECT value FROM json_each(?)) AND ("+where+")", string(list))
	return rows, err
}

// listIn is the metas of root's run folders that match where, newest first.
func listIn(root, where string) ([]Meta, error) {
	db, err := runsDB()
	if err != nil {
		return nil, err
	}
	ids, err := runFolders(db, root)
	if err != nil {
		return nil, err
	}
	rows, err := metasIn(db, ids, where)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, id := range ids {
		if m, ok := rows[id]; ok {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// Running returns the live runs whose meta says running, newest first.
func Running() ([]Meta, error) { return listIn(RunsDir(), "status = 'running'") }

// Changes keeps a run index current without reading every row: the live run
// folders, the rows written after a revision, and the revision to ask from
// next time.
type Changes struct {
	Live    []string
	Changed map[string]Meta
	Rev     int64
}

// ChangesSince returns the live runs and the rows written after rev.
func ChangesSince(rev int64) (Changes, error) {
	db, err := runsDB()
	if err != nil {
		return Changes{}, err
	}
	ids, err := runFolders(db, RunsDir())
	if err != nil {
		return Changes{}, err
	}
	rows, top, err := metasWhere(db, "rev > ?", rev)
	if err != nil {
		return Changes{}, err
	}
	return Changes{Live: ids, Changed: rows, Rev: max(rev, top)}, nil
}

// endRun moves a running run to a final status. Only a running run changes,
// so a cancel racing the run's own Finish cannot undo it: the first wins. It
// reports whether this call ended the run, and the meta as stored.
func endRun(dir, id, status, errMsg string, ended time.Time) (Meta, bool, error) {
	var meta Meta
	var changed bool
	_, err := changeMeta(dir, id, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		meta, changed, err = endRunTx(ctx, conn, id, status, errMsg, ended)
		return err
	})
	return meta, changed, err
}

// setPaused sets or clears a running run's paused field.
func setPaused(dir, id string, paused bool) error {
	body := `json_remove(body,'$.paused')`
	if paused {
		body = `json_set(body,'$.paused',json('true'))`
	}
	_, err := changeMeta(dir, id, func(ctx context.Context, conn *sql.Conn) error {
		rev, err := nextRev(ctx, conn)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `UPDATE runs SET body=`+body+`, rev=? WHERE id=? AND status='running'`, rev, id)
		return err
	})
	return err
}

// IsPaused reads the canonical pause state of a running run.
func IsPaused(id string) bool {
	db, err := runsDB()
	if err != nil {
		return false
	}
	var paused bool
	err = db.QueryRow(`SELECT coalesce(json_extract(body,'$.paused'),0) FROM runs WHERE id=? AND status='running'`, id).Scan(&paused)
	return err == nil && paused
}

// deleteMeta removes a run's rows (meta, steering and ancestry) and their
// copies' stamps, in one write.
func deleteMeta(db *sql.DB, id string) error {
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "DELETE FROM runs WHERE id=?", id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM run_agent_steering WHERE run=?", id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM run_directories WHERE run_id=?", id); err != nil {
			return err
		}
		// The meta's and the workers' steering copies are all under runs/<id>.
		if err := state.ForgetMirrors(ctx, conn, "runs/"+id); err != nil {
			return err
		}
		return ancestry.ForgetAncestry(ctx, conn, id)
	})
}

// endRunTx is the shared first-terminal-state transition. The caller owns its transaction.
func endRunTx(ctx context.Context, conn *sql.Conn, id, status, errMsg string, ended time.Time) (Meta, bool, error) {
	previous, err := LoadStored(ctx, conn, id)
	if err != nil {
		return Meta{}, false, err
	}
	if previous.ID != id {
		return Meta{}, false, fmt.Errorf("stored run identity does not match %s", id)
	}
	if previous.Status != "running" {
		return previous, false, nil
	}
	at, err := ended.Round(0).MarshalJSON()
	if err != nil {
		return Meta{}, false, err
	}
	rev, err := nextRev(ctx, conn)
	if err != nil {
		return Meta{}, false, err
	}
	result, err := conn.ExecContext(ctx, `UPDATE runs SET body=json_remove(json_set(body,'$.status',?,'$.endedAt',json(?),'$.error',?),'$.paused'), rev=?
WHERE id=? AND status='running'`, status, string(at), errMsg, rev, id)
	if err != nil {
		return Meta{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Meta{}, false, err
	}
	meta, err := LoadStored(ctx, conn, id)
	return meta, changed == 1, err
}
