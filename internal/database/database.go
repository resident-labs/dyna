// Package database opens Dyna's shared SQLite database.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Writers queue in Go before taking a connection; WAL readers stay parallel.
var writerLocks sync.Map // *sql.DB -> chan struct{} (capacity 1), so a wait can end with its context

var handles = struct {
	sync.Mutex
	byFolder map[string]*sql.DB
	// byName finds a handle by the absolute path a caller passed, without
	// resolving it again: stores call Open on every query. Relative names are
	// not kept, because they mean another folder after a change of working
	// folder. Known limit: if a data folder symlink is pointed elsewhere while
	// the process runs, its name keeps the database it first resolved to.
	byName map[string]*sql.DB
}{byFolder: map[string]*sql.DB{}, byName: map[string]*sql.DB{}}

// Open shares one handle per real data folder. The folder's owner must stop
// its users before calling Close. Store methods do not close shared handles.
func Open(dataDir string) (*sql.DB, error) {
	var db *sql.DB
	if filepath.IsAbs(dataDir) {
		handles.Lock()
		db = handles.byName[dataDir]
		handles.Unlock()
		if db != nil {
			return db, nil
		}
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if migrates.Load() {
		if err := os.MkdirAll(absolute, 0700); err != nil {
			return nil, err
		}
	}
	folder, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %v", errNotMigrated, err)
	} else if err != nil {
		return nil, err
	}
	handles.Lock()
	defer handles.Unlock()
	if db = handles.byFolder[folder]; db == nil {
		if db, err = openDatabase(folder); err != nil {
			return nil, err
		}
		handles.byFolder[folder] = db
	}
	if filepath.IsAbs(dataDir) {
		handles.byName[dataDir] = db
	}
	return db, nil
}

// closeHooks run when a shared handle closes, so packages that keep state per
// handle (internal/state's writers) can drop it. internal/state cannot be
// imported here.
var closeHooks struct {
	sync.Mutex
	fns []func(*sql.DB)
}

// OnClose runs fn with each shared handle that Close releases. fn runs while
// Close holds its locks, so it must be quick and must not call back into
// this package.
func OnClose(fn func(*sql.DB)) {
	closeHooks.Lock()
	closeHooks.fns = append(closeHooks.fns, fn)
	closeHooks.Unlock()
}

// closingHooks run with each shared handle that Close is about to release,
// before the handle closes, so a package that publishes from the handle
// (internal/state's writers) can finish or stop a publish that is already
// running. internal/state cannot be imported here.
var closingHooks struct {
	sync.Mutex
	fns []func(*sql.DB)
}

// OnClosing runs fn with each shared handle that Close is about to release,
// before the handle closes. Close waits for fn, so fn may block until its own
// work on the handle has ended. fn must not call back into this package.
func OnClosing(fn func(*sql.DB)) {
	closingHooks.Lock()
	closingHooks.fns = append(closingHooks.fns, fn)
	closingHooks.Unlock()
}

// Close releases the shared handle after the data folder's users have stopped.
func Close(dataDir string) error {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	folder, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		folder = absolute
	} else if err != nil {
		return err
	}
	handles.Lock()
	db := handles.byFolder[folder]
	delete(handles.byFolder, folder)
	for name, named := range handles.byName {
		if named == db {
			delete(handles.byName, name)
		}
	}
	handles.Unlock()
	if db != nil {
		// A package that publishes from this handle ends or stops its work
		// before the handle closes, so no publish reaches a closed database.
		closingHooks.Lock()
		before := slices.Clone(closingHooks.fns)
		closingHooks.Unlock()
		for _, fn := range before {
			fn(db)
		}
		err := db.Close()
		closeHooks.Lock()
		for _, fn := range closeHooks.fns {
			fn(db)
		}
		closeHooks.Unlock()
		writerLocks.Delete(db)
		backedUp.Delete(db)
		migrations.Range(func(ready, _ any) bool {
			if ready.(migrated).db == db {
				migrations.Delete(ready)
			}
			return true
		})
		return err
	}
	return nil
}

// migrates is true only in the engine and in test binaries (requirement 41).
// Other processes use the tables the engine left and never change the schema.
// This switch is a phase: it goes when store open paths stop calling Migrate
// and only the engine runs migrations (the done tier of item 41).
var migrates atomic.Bool

func init() { migrates.Store(testing.Testing()) }

// OwnSchema makes this process the one that creates and migrates dyna.db.
// The engine calls it before it opens any store.
func OwnSchema() { migrates.Store(true); migrationFailed.Store(false) }

// OwnsSchema reports whether this process creates and migrates dyna.db.
func OwnsSchema() bool { return migrates.Load() }

// DisownSchema gives up schema ownership, for an engine whose migration
// failed and that now runs read-only: it must not migrate a store lazily
// on first open, so it fails like any other process instead.
func DisownSchema() { migrates.Store(false); migrationFailed.Store(true) }

// migrationFailed marks an engine whose own migration failed: nothing is
// updating there, so its errors keep the restart advice alone.
var migrationFailed atomic.Bool

var errNotMigrated = errors.New("only the engine creates and migrates dyna.db; restart it: systemctl --user restart dyna-engine.service")

// olderSchemaError is a store the running engine has not migrated yet: this
// dyna is newer than the engine, as in the minutes while an update restarts it.
// It reads as errNotMigrated and says what to do first.
type olderSchemaError struct {
	key        string
	have, need int
}

func (e olderSchemaError) Error() string {
	if migrationFailed.Load() || e.have == 0 {
		return fmt.Sprintf("%v: %s is version %d, this dyna needs %d", errNotMigrated, e.key, e.have, e.need)
	}
	return fmt.Sprintf("Dyna is updating; retry in a minute (%s is version %d, this dyna needs %d). If it stays this way, restart the engine: systemctl --user restart dyna-engine.service", e.key, e.have, e.need)
}

func (olderSchemaError) Is(target error) bool { return target == errNotMigrated }

// IsOlderSchema reports whether err names an older version of the given store.
func IsOlderSchema(err error, key string) bool {
	var old olderSchemaError
	return errors.As(err, &old) && old.key == key
}

// IsUpdatingSchema excludes an engine whose migration failed: retrying there
// cannot make progress, and must not delay its read-only service.
func IsUpdatingSchema(err error, key string) bool {
	var old olderSchemaError
	return !migrationFailed.Load() && errors.As(err, &old) && old.key == key && old.have > 0
}

func openDatabase(dataDir string) (*sql.DB, error) {
	path, err := filepath.Abs(filepath.Join(dataDir, "dyna.db"))
	if err != nil {
		return nil, err
	}
	if !migrates.Load() {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%w: %v", errNotMigrated, err)
		}
	} else if file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600); err != nil {
		return nil, err
	} else if err := file.Close(); err != nil {
		return nil, err
	}
	query := url.Values{"_pragma": connectionPragmas(testing.Testing())}
	name := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", name.String())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openRetryWait)
	defer cancel()
	// A WAL-mode transition can return SQLITE_BUSY before SQLite invokes its
	// busy handler. Retry only initialization; transactions use busy_timeout.
	// The deadline stops the retries between attempts. It never cancels an
	// attempt in progress: a slow disk can take seconds to sync the first
	// connection's WAL switch, and the open must still succeed. A disk that
	// truly hangs keeps Open waiting; do not add a timeout back, because
	// cancelling never stopped the sync, it only left it running behind.
	err = Retry(ctx, func() error {
		openAttempt(ctx)
		attempt := context.WithoutCancel(ctx)
		var exists bool
		err := db.QueryRowContext(attempt, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='meta')").Scan(&exists)
		if err == nil && !exists && !migrates.Load() {
			err = fmt.Errorf("%w: %s has no meta table", errNotMigrated, path)
		} else if err == nil && !exists {
			err = Immediate(context.Background(), db, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR IGNORE INTO meta(key,value) VALUES ('schema.version','1');`)
				return err
			})
		}
		if err == nil {
			var version string
			err = db.QueryRowContext(attempt, "SELECT value FROM meta WHERE key='schema.version'").Scan(&version)
			if err == nil && version != "1" {
				err = fmt.Errorf("unsupported database schema version %s", version)
			}
		}
		return err
	})
	if err != nil {
		db.Close()
		writerLocks.Delete(db)
		return nil, err
	}
	return db, nil
}

// openRetryWait bounds the SQLITE_BUSY retries of a new handle's setup.
var openRetryWait = 5 * time.Second

// openAttempt runs at the start of each setup attempt. Tests replace it to
// make an attempt outlast the retry deadline.
var openAttempt = func(context.Context) {}

// connectionPragmas sets up each new connection. synchronous comes before
// journal_mode, so the WAL switch of a new database already uses it: a test
// database then pays no disk flush even for that first commit.
func connectionPragmas(inTest bool) []string {
	return []string{fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()), syncPragma(inTest), "journal_mode(WAL)", fmt.Sprintf("wal_autocheckpoint(%d)", backstopPages), fmt.Sprintf("journal_size_limit(%d)", walSizeLimit), "foreign_keys(ON)"}
}

// syncPragma keeps production on WAL with synchronous=NORMAL: a process crash
// loses nothing, and an OS crash can lose only the last commits. Test binaries
// create thousands of throwaway databases, so they skip the disk flush.
func syncPragma(inTest bool) string {
	if inTest {
		return "synchronous(OFF)"
	}
	return "synchronous(NORMAL)"
}

// busyTimeout is how long SQLite waits for a lock inside one call.
var busyTimeout = 5 * time.Second

// beginWait bounds the retries of a busy BEGIN IMMEDIATE in Immediate.
var beginWait = 60 * time.Second

// beginImmediate starts the writer transaction. Tests replace it to make
// BEGIN succeed only after its deadline.
var beginImmediate = func(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	return err
}

// busyRetry runs before each retry of a busy call. Tests use it to observe a
// retry instead of guessing its timing.
var busyRetry = func() {}

// Immediate holds SQLite's writer reservation across a read-modify-write.
// ctx bounds the wait for the connection and the writer lock. Once update
// has run, COMMIT and ROLLBACK ignore ctx, so a write never ends half done.
func Immediate(ctx context.Context, db *sql.DB, update func(*sql.Conn) error) error {
	return ImmediateThen(ctx, db, update, nil)
}

// ImmediateThen is Immediate, and runs committed after the COMMIT while it
// still holds the writer lock, so the callbacks of one handle run in commit
// order. committed must not block.
func ImmediateThen(ctx context.Context, db *sql.DB, update func(*sql.Conn) error, committed func()) error {
	// Before the writer queue: the note may read its report file once.
	noteOutsideWrite(db)
	lock, _ := writerLocks.LoadOrStore(db, make(chan struct{}, 1))
	writer := lock.(chan struct{})
	waiting.Add(1)
	select {
	case writer <- struct{}{}:
		waiting.Add(-1)
	case <-ctx.Done():
		waiting.Add(-1)
		return ctx.Err()
	}
	defer func() { <-writer }()
	if err := transact(ctx, db, update); err != nil {
		return err
	}
	if committed != nil {
		committed()
	}
	return nil
}

// transact runs update in one BEGIN IMMEDIATE transaction. The caller holds
// the writer lock.
func transact(ctx context.Context, db *sql.DB, update func(*sql.Conn) error) error {
	start := now()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Another process can hold the write lock past busy_timeout, for example
	// an agent's CLI starved of CPU on a loaded machine. Nothing is written
	// before BEGIN succeeds, so waiting longer is safe.
	begin, cancel := context.WithTimeout(ctx, beginWait)
	defer cancel()
	// SQLite may complete BEGIN before the driver reports cancellation.
	// Roll it back even on that error, before returning the connection.
	finish := context.WithoutCancel(ctx)
	var began time.Time
	var committed bool
	defer func() {
		conn.ExecContext(finish, "ROLLBACK")
		if !began.IsZero() && !committed {
			writeHold.record(now().Sub(began))
		}
	}()
	err = Retry(begin, func() error { return beginImmediate(begin, conn) })
	if err != nil {
		return err
	}
	// A BEGIN that got the lock only after the deadline does not run the update.
	if err := begin.Err(); err != nil {
		return fmt.Errorf("database stayed busy: %w", err)
	}
	began = now()
	if err := update(conn); err != nil {
		logSlowWrite(began.Sub(start), now().Sub(began), 0)
		return err
	}
	committing := now()
	_, err = conn.ExecContext(finish, "COMMIT")
	body, commit := committing.Sub(began), now().Sub(committing)
	if err == nil {
		writeHold.record(body + commit)
		committed = true
	}
	commitDuration.record(commit)
	logSlowWrite(began.Sub(start), body, commit)
	return err
}

// Migrate brings one store's tables to version, recorded under key in meta.
// When the recorded version is older, apply runs in a writer transaction with
// the old version (0 when absent), and the new version commits with it.
// Before it changes an existing store, it backs up dyna.db.
// A newer recorded version means a newer dyna wrote the store: Migrate fails
// with ErrNewerSchema and changes nothing. Outside the engine an older
// version fails with errNotMigrated, also changing nothing. Each store calls
// Migrate before its first query; later calls on the same handle return at once.
func Migrate(db *sql.DB, key string, version int, apply func(conn *sql.Conn, old int) error) error {
	ready := migrated{db, key}
	if have, ok := migrations.Load(ready); ok && have == version {
		return nil
	}
	// A current store needs no writer. This matters when a store's first
	// query runs inside another store's transaction, which holds the writer.
	have, err := storedVersion(db.QueryRow("SELECT value FROM meta WHERE key=?", key), key)
	switch {
	case err == nil && have == version:
		migrations.Store(ready, version)
		return nil
	case err == nil && have > version:
		return newerSchema(key, have, version)
	case !migrates.Load() && err == nil:
		return olderSchemaError{key: key, have: have, need: version}
	case !migrates.Load():
		return err
	}
	// Changing an existing store's tables takes one backup first. The stores
	// that migrate at one engine start share it (requirement 23).
	if err == nil && have > 0 {
		if err := BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before migrating %s: %w", key, err)
		}
	}
	err = Immediate(context.Background(), db, func(conn *sql.Conn) error {
		ctx := context.Background()
		old, err := storedVersion(conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key), key)
		if err != nil {
			return err
		}
		if old == version {
			return nil
		}
		if old > version {
			return newerSchema(key, old, version)
		}
		if err := apply(conn, old); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, strconv.Itoa(version))
		return err
	})
	if err != nil {
		return err
	}
	migrations.Store(ready, version)
	return nil
}

// storedVersion reads a store's version from its meta row, 0 when none is stored.
func storedVersion(row *sql.Row, key string) (int, error) {
	var value string
	if err := row.Scan(&value); err == sql.ErrNoRows {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	version, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: stored schema version %q is not a number", key, value)
	}
	return version, nil
}

// HasTable reports whether conn's database has the table name. A store
// whose rows another store deletes uses it, because that other store can run
// in a data folder where this store's migration has not run yet.
func HasTable(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	return n > 0, err
}

// ErrNewerSchema reports state written by a newer dyna than this binary.
// A long-lived process that serves such state, like a session's shell MCP
// server, restarts on the current binary instead of failing every call.
var ErrNewerSchema = errors.New("dyna.db was written by a newer dyna; update dyna")

// NewerSchema reports whether err says the state this binary must serve was
// written by a newer dyna.
func NewerSchema(err error) bool { return errors.Is(err, ErrNewerSchema) }

func newerSchema(key string, have, known int) error {
	return fmt.Errorf("%w: %s is version %d, this dyna knows up to %d", ErrNewerSchema, key, have, known)
}

type migrated struct {
	db  *sql.DB
	key string
}

var migrations sync.Map // migrated -> version (int)

// busyCount counts transient lock answers (SQLITE_BUSY, SQLITE_PROTOCOL)
// that were retried, for the engine's Health call.
var busyCount atomic.Int64

// BusyRetries returns how many transient SQLite lock answers this process
// retried.
func BusyRetries() int64 { return busyCount.Load() }

// waiting counts writers that wait for the in-process writer lock.
var waiting atomic.Int64

// Waiting returns how many writers in this process wait for the writer
// lock now, for the engine's Health call.
func Waiting() int64 { return waiting.Load() }

// SQLite's primary result codes for a database another connection holds.
const (
	sqliteBusy   = 5
	sqliteLocked = 6
	// sqliteProtocol is WAL's answer when its wal-index stayed contended past
	// SQLite's own retries, under IO pressure (D-448).
	sqliteProtocol = 15
)

// primaryCode is err's SQLite primary result code, or 0 when err does not
// come from SQLite. Tests replace it to feed a result code through the retry
// paths, because a wal-index contended enough to answer SQLITE_PROTOCOL
// cannot be staged deterministically.
var primaryCode = func(err error) int {
	var sqliteError *sqlite.Error
	if !errors.As(err, &sqliteError) {
		return 0
	}
	return sqliteError.Code() & 0xff
}

// Busy reports whether err is SQLite's busy or locked answer, which passes
// when the other connection ends its transaction.
func Busy(err error) bool {
	code := primaryCode(err)
	return code == sqliteBusy || code == sqliteLocked
}

// Retry always makes the first attempt, and checks ctx only after a
// transient lock answer, so a deadline stops retries but never prevents the
// first try. A transient lock answer is SQLITE_BUSY, or WAL's SQLITE_PROTOCOL,
// which a wal-index contended past SQLite's own retries returns; both pass
// when the other connection ends its transaction or lets the wal-index
// settle. try must repeat its whole read, because a retry runs it from the
// start. When the deadline ends first, the returned error keeps both the
// cancellation and the last SQLite answer.
func Retry(ctx context.Context, try func() error) error {
	for {
		err := try()
		if code := primaryCode(err); code != sqliteBusy && code != sqliteProtocol {
			return err
		}
		busyCount.Add(1)
		busyRetry()
		select {
		case <-ctx.Done():
			return fmt.Errorf("database stayed busy: %w: %w", ctx.Err(), err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
