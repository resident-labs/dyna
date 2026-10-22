package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSQLiteInitializationDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	want := fmt.Errorf("invalid database")
	err := retryBusy(context.Background(), func() error { calls++; return want })
	if err != want || calls != 1 {
		t.Fatalf("unexpected retry: %d calls, %v", calls, err)
	}
}

func TestOpenSharesOneHandleForCanonicalDataFolder(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(root)
	second, err := Open(alias)
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatal("database reopened for the same data folder")
	}
}

func TestImmediateQueuesWritersWithoutSQLiteBusyWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dyna.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer writerLocks.Delete(db)
	if _, err := db.Exec("CREATE TABLE counter(value INTEGER); INSERT INTO counter VALUES(0)"); err != nil {
		t.Fatal(err)
	}
	const writers = 16
	start := make(chan struct{})
	results := make(chan error, writers)
	for range writers {
		go func() {
			<-start
			results <- Immediate(context.Background(), db, func(conn *sql.Conn) error {
				var value int
				if err := conn.QueryRowContext(context.Background(), "SELECT value FROM counter").Scan(&value); err != nil {
					return err
				}
				// Give another caller a turn while this transaction owns the write lock.
				runtime.Gosched()
				_, err := conn.ExecContext(context.Background(), "UPDATE counter SET value=?", value+1)
				return err
			})
		}()
	}
	close(start)
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for range writers {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("writer contention reached SQLite: %v", err)
			}
		case <-deadline.C:
			t.Fatal("writers did not finish")
		}
	}
	var count int
	if err := db.QueryRow("SELECT value FROM counter").Scan(&count); err != nil || count != writers {
		t.Fatalf("committed writes: %d, %v; want %d", count, err, writers)
	}
}

func TestMigrateAppliesEachVersionOnceAndRollsBackFailures(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(dir)
	var seen []int
	create := func(conn *sql.Conn, old int) error {
		seen = append(seen, old)
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS proof(value TEXT)")
		return err
	}
	for range 2 {
		if err := Migrate(db, "proof.version", 1, create); err != nil {
			t.Fatal(err)
		}
	}
	// A restarted engine reads the recorded version and does not apply again.
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	if db, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, "proof.version", 1, create); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[0]" {
		t.Fatalf("applied with old versions %v, want once from none (0)", seen)
	}
	failed := fmt.Errorf("import failed")
	err = Migrate(db, "proof.version", 2, func(conn *sql.Conn, old int) error {
		if _, err := conn.ExecContext(context.Background(), "DROP TABLE proof"); err != nil {
			return err
		}
		return failed
	})
	if err != failed {
		t.Fatalf("Migrate error = %v, want %v", err, failed)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='proof.version'").Scan(&version); err != nil || version != "1" {
		t.Fatalf("version after failure = %q, %v; want 1", version, err)
	}
	if _, err := db.Exec("SELECT count(*) FROM proof"); err != nil {
		t.Fatalf("failed apply was not rolled back: %v", err)
	}
	if err := Migrate(db, "proof.version", 2, create); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[0 1]" {
		t.Fatalf("applied with old versions %v, want 0 then 1", seen)
	}
}

// Another process can hold the write lock past busy_timeout, for example an
// agent's CLI starved of CPU. Immediate retries instead of failing the
// caller's write with SQLITE_BUSY. The lock is released only after a retry
// was observed.
func TestImmediateWaitsPastBusyTimeoutForAnotherWriter(t *testing.T) {
	setBusy(t, 20*time.Millisecond, time.Minute)
	retried := make(chan struct{}, 1)
	oldRetry := busyRetry
	busyRetry = func() {
		select {
		case retried <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { busyRetry = oldRetry })
	db, other := lockedPair(t)
	before := BusyRetries()
	done := make(chan error, 1)
	go func() { done <- Immediate(context.Background(), db, createProof) }()
	select {
	case <-retried:
		if BusyRetries() <= before {
			t.Fatal("a retried SQLITE_BUSY was not counted for Health")
		}
	case err := <-done:
		t.Fatalf("Immediate returned while another writer held the lock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Immediate never retried the busy BEGIN")
	}
	if _, err := other.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Immediate failed after the other writer committed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Immediate did not finish after the lock was released")
	}
}

// A BEGIN that gets the lock only after its deadline must not run the update,
// and the transaction must release the lock. The test's BEGIN waits for the
// deadline itself, so the outcome does not depend on timing.
func TestImmediateDoesNotRunTheUpdateAfterItsDeadline(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	setBusy(t, busyTimeout, 10*time.Millisecond)
	old := beginImmediate
	beginImmediate = func(ctx context.Context, conn *sql.Conn) error {
		<-ctx.Done()
		_, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		return err
	}
	t.Cleanup(func() { beginImmediate = old })
	ran := false
	err = Immediate(context.Background(), db, func(conn *sql.Conn) error { ran = true; return createProof(conn) })
	if err == nil || ran {
		t.Fatalf("Immediate after its deadline: err=%v, update ran=%v; want an error and no update", err, ran)
	}
	other, err := sql.Open("sqlite", filepath.Join(dir, "dyna.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec("BEGIN IMMEDIATE; ROLLBACK"); err != nil {
		t.Fatalf("Immediate kept the write lock after giving up: %v", err)
	}
}

// The driver can report cancellation after SQLite has executed BEGIN.
// Reuse the same connection to prove the failed call left no transaction.
func TestImmediateRollsBackWhenBeginReportsCancellation(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { Close(dir) })
			db.SetMaxOpenConns(1)
			old := beginImmediate
			t.Cleanup(func() { beginImmediate = old })
			beginImmediate = func(_ context.Context, conn *sql.Conn) error {
				if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
					return err
				}
				return want
			}
			ran := false
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			err = Immediate(ctx, db, func(*sql.Conn) error { ran = true; return nil })
			if !errors.Is(err, want) || ran {
				t.Fatalf("failed BEGIN: error %v, update ran %v; want %v and no update", err, ran, want)
			}
			beginImmediate = old
			if err := Immediate(ctx, db, createProof); err != nil {
				t.Fatalf("next transaction on the same connection: %v", err)
			}
		})
	}
}

func setBusy(t *testing.T, timeout, wait time.Duration) {
	t.Helper()
	oldTimeout, oldWait := busyTimeout, beginWait
	busyTimeout, beginWait = timeout, wait
	t.Cleanup(func() { busyTimeout, beginWait = oldTimeout, oldWait })
}

// lockedPair opens a fresh shared handle and a second connection that holds
// the write lock.
func lockedPair(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	other, err := sql.Open("sqlite", filepath.Join(dir, "dyna.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	other.SetMaxOpenConns(1)
	if _, err := other.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	return db, other
}

func createProof(conn *sql.Conn) error {
	_, err := conn.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS proof(v TEXT)")
	return err
}

// A writer that waits behind another writer stops when its context ends.
func TestImmediateWaitEndsWithContext(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dyna.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer writerLocks.Delete(db)
	holding, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- Immediate(context.Background(), db, func(*sql.Conn) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	ran := false
	go func() { second <- Immediate(ctx, db, func(*sql.Conn) error { ran = true; return nil }) }()
	cancel()
	if err := <-second; err != context.Canceled || ran {
		t.Fatalf("waiting writer returned %v (ran %v); want context.Canceled without running", err, ran)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// Writers waiting behind the writer lock are counted for Health, and the
// count drops when they get the lock or give up.
func TestWaitingCountsWritersBehindTheLock(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dyna.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer writerLocks.Delete(db)
	holding, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- Immediate(context.Background(), db, func(*sql.Conn) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	before := Waiting()
	ctx, cancel := context.WithCancel(context.Background())
	const waiters = 3
	results := make(chan error, waiters+1)
	for range waiters {
		go func() { results <- Immediate(context.Background(), db, func(*sql.Conn) error { return nil }) }()
	}
	go func() { results <- Immediate(ctx, db, func(*sql.Conn) error { return nil }) }()
	for Waiting() != before+waiters+1 {
		runtime.Gosched() // the waiters have no event of their own; the test deadline bounds this
	}
	cancel()
	if err := <-results; err != context.Canceled {
		t.Fatalf("cancelled waiter returned %v", err)
	}
	if got := Waiting(); got != before+waiters {
		t.Fatalf("Waiting() = %d after one waiter gave up; want %d", got, before+waiters)
	}
	close(release)
	for range waiters {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if got := Waiting(); got != before {
		t.Fatalf("Waiting() = %d after all writers ran; want %d", got, before)
	}
}
