package database

import (
	"context"
	"slices"
	"testing"
	"time"
)

// The retry deadline of a new handle's setup stops retries between attempts,
// but never cancels an attempt in progress. On a slow disk, the first
// connection's WAL switch can sync for longer than the deadline, and the open
// must still succeed.
func TestOpenFinishesAnAttemptThatOutlastsItsDeadline(t *testing.T) {
	oldWait, oldAttempt := openRetryWait, openAttempt
	openRetryWait = time.Millisecond
	openAttempt = func(ctx context.Context) { <-ctx.Done() } // the attempt runs past the deadline
	t.Cleanup(func() { openRetryWait, openAttempt = oldWait, oldAttempt })
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open with a slow first attempt = %v, want success", err)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='schema.version'").Scan(&version); err != nil || version != "1" {
		t.Fatalf("schema version = %q, %v", version, err)
	}
}

// synchronous is set before journal_mode, so even a new database's WAL switch
// uses it, and a test database pays no disk flush for that commit.
func TestSyncPragmaComesBeforeTheWALSwitch(t *testing.T) {
	for _, inTest := range []bool{true, false} {
		p := connectionPragmas(inTest)
		sync, wal := slices.Index(p, syncPragma(inTest)), slices.Index(p, "journal_mode(WAL)")
		if sync < 0 || wal < 0 || sync > wal {
			t.Errorf("connectionPragmas(%v) = %q; want synchronous before journal_mode", inTest, p)
		}
	}
}

// A deadline that has already passed still allows the first attempt: it
// stops only the retries after an SQLITE_BUSY failure.
func TestRetryBusyAlwaysMakesTheFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if err := Retry(ctx, func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("Retry on an ended context = %v after %d attempts, want one successful attempt", err, calls)
	}
}
