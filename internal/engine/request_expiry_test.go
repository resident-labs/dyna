package engine

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func seedExpirySweep(t *testing.T, db *sql.DB, cutoff time.Time, count int) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(t.Context(), "INSERT INTO request_ids(id,method,done_at) VALUES(?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := range count {
		if _, err := stmt.ExecContext(t.Context(), fmt.Sprintf("expired-%03d", i), "RunnerClaimInput", cutoff.Add(-time.Millisecond).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func expiredRequestCount(t *testing.T, db *sql.DB, cutoff time.Time) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM request_ids WHERE done_at < ?", cutoff.UnixMilli()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Unlike advance, fire exactly one callback so a foreground write can run
// between maintenance batches. Reuse the existing fake clock and timer set.
func fireExpiryTimer(t *testing.T, clock *fakeClock) {
	t.Helper()
	clock.mu.Lock()
	if len(clock.pending) != 1 {
		n := len(clock.pending)
		clock.mu.Unlock()
		t.Fatalf("pending expiry timers = %d, want 1", n)
	}
	next := clock.pending[0]
	clock.pending = nil
	if next.at.After(clock.now) {
		clock.now = next.at
	}
	clock.mu.Unlock()
	next.fn()
}

func TestRequestExpirySweepYieldsAndKeepsOneCutoff(t *testing.T) {
	db, w, _ := thingsStore(t)
	clock := newFakeClock()
	w.Now = clock.Now
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	set := clock.timers(ctx)
	defer set.close()
	cutoff := clock.Now().Add(-state.RequestKeep)
	seedExpirySweep(t, db, cutoff, 259)
	if _, err := db.Exec("INSERT INTO request_ids(id,method,done_at) VALUES('ages-during-sweep','RunnerClaimInput',?)", cutoff.Add(500*time.Millisecond).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	before := w.Health().Writes
	pruneRequests(ctx, set, w)
	if n := expiredRequestCount(t, db, cutoff); n != 131 {
		t.Fatalf("startup batch left %d expired rows, want 131", n)
	}
	if writes := w.Health().Writes - before; writes != 1 {
		t.Fatalf("startup callback made %d writer calls, want 1", writes)
	}
	if !clock.pendingAt(clock.Now()) {
		t.Fatal("next batch was not handed back to the engine timer set")
	}
	if err := w.Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), "INSERT INTO things(name) VALUES('foreground')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db); n != 1 {
		t.Fatalf("foreground write between batches stored %d rows", n)
	}
	// Move time without firing the next callback: its cutoff must stay fixed.
	clock.mu.Lock()
	clock.now = clock.now.Add(time.Second)
	clock.mu.Unlock()
	fireExpiryTimer(t, clock)
	if n := expiredRequestCount(t, db, cutoff); n != 3 {
		t.Fatalf("second batch left %d expired rows, want 3", n)
	}
	fireExpiryTimer(t, clock)
	if n := expiredRequestCount(t, db, cutoff); n != 0 {
		t.Fatalf("third batch left %d expired rows", n)
	}
	if writes := w.Health().Writes - before; writes != 4 {
		t.Fatalf("three prune batches and foreground write made %d writer calls", writes)
	}
	fireExpiryTimer(t, clock)
	if writes := w.Health().Writes - before; writes != 4 {
		t.Fatalf("empty completion check added a writer call: %d", writes)
	}
	if !clock.pendingAt(clock.Now().Add(time.Hour)) {
		t.Fatal("completed sweep did not retain hourly expiry")
	}
	var at int64
	if err := db.QueryRow("SELECT done_at FROM request_ids WHERE id='ages-during-sweep'").Scan(&at); err != nil || at != cutoff.Add(500*time.Millisecond).UnixMilli() {
		t.Fatalf("row that aged during the sweep was changed: %d, %v", at, err)
	}
	fireExpiryTimer(t, clock)
	if n := expiredRequestCount(t, db, clock.Now().Add(-state.RequestKeep)); n != 0 {
		t.Fatalf("next hourly sweep left %d expired rows", n)
	}
}

func TestRequestExpirySweepCancellationStopsWork(t *testing.T) {
	for _, beforeStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("before_start_%v", beforeStart), func(t *testing.T) {
			db, w, _ := thingsStore(t)
			clock := newFakeClock()
			start := clock.Now()
			w.Now = func() time.Time { return start }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			set := clock.timers(ctx)
			defer set.close()
			cutoff := clock.Now().Add(-state.RequestKeep)
			seedExpirySweep(t, db, cutoff, 129)
			if beforeStart {
				cancel()
			}
			pruneRequests(ctx, set, w)
			wantRows, wantWrites := 1, 1
			if beforeStart {
				wantRows, wantWrites = 129, 0
			}
			if rows := expiredRequestCount(t, db, cutoff); rows != wantRows {
				t.Fatalf("cancelled sweep left %d rows, want %d", rows, wantRows)
			}
			cancel()
			guard := time.NewTimer(30 * time.Second)
			defer guard.Stop()
			select {
			case <-set.done:
			case <-guard.C:
				t.Fatal("cancelled timer set did not stop")
			}
			clock.advance(time.Hour)
			if writes := w.Health().Writes; writes != wantWrites {
				t.Fatalf("cancelled sweep made %d writer calls", writes)
			}
			if rows := expiredRequestCount(t, db, cutoff); rows != wantRows {
				t.Fatalf("cancelled continuation changed rows: %d", rows)
			}
		})
	}
}

func TestRequestExpirySweepErrorWaitsForNextHour(t *testing.T) {
	db, w, _ := thingsStore(t)
	clock := newFakeClock()
	w.Now = clock.Now
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	set := clock.timers(ctx)
	defer set.close()
	cutoff := clock.Now().Add(-state.RequestKeep)
	seedExpirySweep(t, db, cutoff, 259)
	if _, err := db.Exec("CREATE TRIGGER fail_expiry BEFORE DELETE ON request_ids BEGIN SELECT RAISE(ABORT, 'expiry blocked'); END"); err != nil {
		t.Fatal(err)
	}
	pruneRequests(ctx, set, w)
	if !clock.pendingAt(clock.Now().Add(time.Hour)) {
		t.Fatal("failed batch did not return to the hourly schedule")
	}
	clock.mu.Lock()
	pending := len(clock.pending)
	clock.mu.Unlock()
	if pending != 1 || w.Health().Writes != 1 {
		t.Fatalf("failed batch scheduled %d timers and made %d writer calls", pending, w.Health().Writes)
	}
	if n := expiredRequestCount(t, db, cutoff); n != 259 {
		t.Fatalf("failed batch removed rows: %d remain", n)
	}
}
