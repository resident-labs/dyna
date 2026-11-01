// Package store owns the v2 SQLite database.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound reports a missing store record.
var ErrNotFound = sql.ErrNoRows

type ErrorCode string

const (
	StoreFromV1 ErrorCode = "store_from_v1"
	StoreTooOld ErrorCode = "store_too_old"
)

type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string { return e.Message }

type request struct {
	ctx  context.Context
	fn   func(*sql.Tx) error
	done chan error
}
type Store struct {
	backupMu      sync.Mutex
	lock          *os.File
	writer        *sql.DB
	read          *sql.DB
	checkpoint    *sql.DB
	checkpointRun func() error
	requests      chan request
	stop          chan struct{}
	done          chan struct{}
	checkDone     chan struct{}
	mu            sync.Mutex
	dispatchMu    sync.RWMutex
	draining      bool
	errMu         sync.Mutex
	closed        bool
	first         error
	currentTx     *sql.Tx
	pending       []contract.Event
	wakePending   []contract.SessionID
	wake          func([]contract.SessionID, []contract.Event)
	subMu         sync.Mutex
	subs          map[chan contract.Event]struct{}
}

// Open creates or opens a store in dir.
func Open(dir string) (*Store, error) {
	return open(dir, floorVersion, floorCommit, nil)
}
func open(dir string, floor int, commit string, checkpointRun func() error) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "store.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = lock.Close()
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("data folder is in use by another engine")
		}
		return nil, err
	}
	path := filepath.Join(dir, "store.db")
	_, err = os.Stat(path)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, err
	}
	if fresh {
		old := filepath.Join(dir, "dyna.db")
		if _, err := os.Stat(old); err == nil {
			return nil, &Error{Code: StoreFromV1, Message: fmt.Sprintf("This folder holds a Dyna v1 store (%s). Dyna v2 cannot open it, and there is no way back to v1 after you import. The main branch has no v1 code. To move your data: (1) stop the v1 engine and capture a backup; (2) build the exporter from tag `v1-final`: `git clone --branch v1-final https://github.com/resident-labs/dyna && cd dyna/cmd/dyna-export && go build -o dyna-export .`; (3) run `./dyna-export` with the flags in its README.md; (4) run `dyna import` on the bundle. See docs/migrate.md.", old)}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := initialize(dir, path); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + url.PathEscape(path) + "?_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=wal_autocheckpoint(0)&_pragma=journal_size_limit(67108864)"
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { writer.Close(); return nil, err }
	var format string
	if err := writer.QueryRow("SELECT value FROM meta WHERE key='format'").Scan(&format); err != nil {
		return fail(fmt.Errorf("invalid store format: %w", err))
	}
	if format != "dyna-v2" {
		return fail(fmt.Errorf("invalid store format %q", format))
	}
	if err := upgrade(writer, floor, commit, steps, func(version int) error {
		_, err := backup(context.Background(), writer, dir, fmt.Sprintf("pre-upgrade-v%d-", version), "pre-upgrade-v*-*.db", time.Now())
		return err
	}); err != nil {
		return fail(err)
	}
	if err := compareSchema(writer); err != nil {
		return fail(err)
	}
	if _, err := writer.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fail(err)
	}
	read, err := sql.Open("sqlite", dsn+"&mode=ro")
	if err != nil {
		return fail(err)
	}
	checkpoint, err := sql.Open("sqlite", dsn)
	if err != nil {
		read.Close()
		return fail(err)
	}
	checkpoint.SetMaxOpenConns(1)
	s := &Store{lock: lock, writer: writer, read: read, checkpoint: checkpoint, requests: make(chan request), stop: make(chan struct{}), done: make(chan struct{}), checkDone: make(chan struct{})}
	if checkpointRun == nil {
		checkpointRun = func() error { _, err := s.checkpoint.Exec("PRAGMA wal_checkpoint(PASSIVE)"); return err }
	}
	s.checkpointRun = checkpointRun
	owned = true
	go s.writeLoop()
	go s.checkLoop(path)
	return s, nil
}

// initialize publishes only a complete database. A crash before publication
// leaves an unrelated temporary file, not a partially initialized store.db.
func initialize(dir, path string) error {
	f, err := os.CreateTemp(dir, ".store-init-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(temp)+"?_pragma=synchronous(OFF)&_pragma=journal_mode(OFF)")
	if err != nil {
		return err
	}
	if _, err = db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err == nil {
		var tx *sql.Tx
		tx, err = db.Begin()
		if err == nil {
			_, err = tx.Exec(schema)
			if err == nil {
				var id [16]byte
				_, err = rand.Read(id[:])
				if err == nil {
					_, err = tx.Exec("INSERT INTO meta(key,value) VALUES ('format','dyna-v2'),('schema_version',?),('store_id',?),('created_commit','')", strconv.Itoa(schemaVersion), hex.EncodeToString(id[:]))
				}
			}
			if err != nil {
				_ = tx.Rollback()
			} else {
				err = tx.Commit()
			}
		}
	}
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return os.Link(temp, path)
}

func compareSchema(db *sql.DB) error {
	expected, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer expected.Close()
	if _, err = expected.Exec(schema); err != nil {
		return err
	}
	query := "SELECT type, name, sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type, name"
	readSchema := func(db *sql.DB) ([]string, error) {
		rows, err := db.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var t, n, s string
			if err := rows.Scan(&t, &n, &s); err != nil {
				return nil, err
			}
			if n == "deliveries" || n == "operations" {
				s = strings.Replace(s, `"`+n+`"`, n, 1)
			}
			out = append(out, t+"\x00"+n+"\x00"+s)
		}
		return out, rows.Err()
	}
	a, err := readSchema(db)
	if err != nil {
		return err
	}
	b, err := readSchema(expected)
	if err != nil {
		return err
	}
	if len(a) != len(b) {
		return errors.New("store schema differs from schema.sql")
	}
	for i := range a {
		if a[i] != b[i] {
			return errors.New("store schema differs from schema.sql")
		}
	}
	return nil
}

// Write submits one atomic transaction to the single writer.
func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("store closed")
	}
	r := request{ctx: ctx, fn: fn, done: make(chan error, 1)}
	select {
	case s.requests <- r:
		s.mu.Unlock()
	case <-ctx.Done():
		s.mu.Unlock()
		return ctx.Err()
	}
	return <-r.done
}
func (s *Store) writeLoop() {
	defer close(s.done)
	for r := range s.requests {
		tx, err := s.writer.BeginTx(r.ctx, nil)
		writerErr := err
		if err == nil {
			s.currentTx = tx
			s.pending = nil
			s.wakePending = nil
			err = r.fn(tx)
			if err != nil {
				_ = tx.Rollback()
			} else {
				err = tx.Commit()
				writerErr = err
				// database/sql rolls the transaction back when the caller's context ends,
				// so a commit that lost that race is the caller's cancel, not a writer fault.
				// Other commit errors stay writer errors, even when the context is also done.
				if errors.Is(err, sql.ErrTxDone) && r.ctx.Err() != nil {
					err = r.ctx.Err()
					writerErr = nil
				}
			}
		}
		s.currentTx = nil
		if err == nil {
			s.publish(s.pending)
			if s.wake != nil {
				s.wake(s.wakePending, s.pending)
			}
		}
		s.pending = nil
		if writerErr != nil && !errors.Is(writerErr, context.Canceled) && !errors.Is(writerErr, context.DeadlineExceeded) {
			s.errMu.Lock()
			if s.first == nil {
				s.first = writerErr
			}
			s.errMu.Unlock()
		}
		r.done <- err
	}
}
func (s *Store) Read() *sql.DB { return s.read }
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("store closed")
	}
	s.closed = true
	close(s.requests)
	s.mu.Unlock()
	<-s.done
	s.subMu.Lock()
	for ch := range s.subs {
		close(ch)
		delete(s.subs, ch)
	}
	s.subMu.Unlock()
	close(s.stop)
	<-s.checkDone
	err := s.first
	for _, db := range []*sql.DB{s.read, s.checkpoint, s.writer} {
		if e := db.Close(); err == nil {
			err = e
		}
	}
	if e := s.lock.Close(); err == nil {
		err = e
	}
	return err
}

// SetWake installs the session notification callback before writes begin.
func (s *Store) SetWake(wake func([]contract.SessionID, []contract.Event)) { s.wake = wake }

// Done closes when the store writer stops.
func (s *Store) Done() <-chan struct{} { return s.done }
