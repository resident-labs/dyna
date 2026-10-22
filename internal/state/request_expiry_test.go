package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func seedExpiredRequests(t *testing.T, db *sql.DB, cutoff time.Time, count int) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(t.Context(), "INSERT INTO request_ids(id,method,result,fingerprint,done_at) VALUES(?,?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := range count {
		if _, err := stmt.ExecContext(t.Context(), fmt.Sprintf("expired-%03d", i), "RunnerClaimInput", []byte(`{"cursor":7}`), []byte("expired-fingerprint"), cutoff.Add(-time.Millisecond).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestExpiryBoundsEachBatchAndKeepsReplay(t *testing.T) {
	w, _ := requestWriter(t)
	cutoff := time.Unix(1_800_000_000, 0)
	w.Now = func() time.Time { return cutoff.Add(RequestKeep) }
	seedExpiredRequests(t, w.db, cutoff, 259)
	kept := []struct {
		id     string
		at     time.Time
		result []byte
	}{
		{"input", cutoff, []byte(" \n{\"posts\":[{\"id\":\"input-1\"}],\"cursor\":7}\t")},
		{"control", cutoff.Add(time.Millisecond), []byte("\n{\"cursor\":7}\t")},
	}
	fingerprint := []byte("retained-fingerprint")
	for _, row := range kept {
		if _, err := w.db.ExecContext(t.Context(), "INSERT INTO request_ids(id,method,result,fingerprint,done_at) VALUES(?,?,?,?,?)", row.id, "RunnerClaimInput", row.result, fingerprint, row.at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []int64{128, 128, 3, 0} {
		before := w.Health().Writes
		n, err := w.PruneRequests(t.Context(), cutoff)
		if err != nil || n != want {
			t.Fatalf("batch %d removed %d: %v; want %d", i+1, n, err, want)
		}
		writes := w.Health().Writes - before
		if want > 0 && writes != 1 || want == 0 && writes != 0 {
			t.Fatalf("batch %d made %d writer calls for %d removals", i+1, writes, n)
		}
	}
	for _, row := range kept {
		ctx := ForRequestCall(t.Context(), row.id, "RunnerClaimInput", fingerprint)
		result, done, err := w.RequestDone(ctx, row.id)
		if err != nil || !done || !bytes.Equal(result, row.result) {
			t.Fatalf("retained %s replay = %q, %v, %v", row.id, result, done, err)
		}
		changed := ForRequestCall(t.Context(), row.id, "RunnerClaimInput", []byte("changed"))
		if _, _, err := w.RequestDone(changed, row.id); !errors.Is(err, ErrRequestConflict) {
			t.Fatalf("retained %s changed fingerprint = %v", row.id, err)
		}
		var at int64
		var savedFingerprint []byte
		if err := w.db.QueryRowContext(t.Context(), "SELECT done_at,fingerprint FROM request_ids WHERE id=?", row.id).Scan(&at, &savedFingerprint); err != nil {
			t.Fatal(err)
		}
		if at != row.at.UnixMilli() || !bytes.Equal(savedFingerprint, fingerprint) {
			t.Fatalf("retained %s metadata changed", row.id)
		}
	}
}

func TestRequestExpiryEmptyCheckMakesNoWrite(t *testing.T) {
	w, _ := requestWriter(t)
	before := w.Health().Writes
	if n, err := w.PruneRequests(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("empty prune = %d, %v", n, err)
	}
	if writes := w.Health().Writes - before; writes != 0 {
		t.Fatalf("empty indexed check made %d writer calls", writes)
	}
}

func TestRequestExpiryCancelledBeforeBatchMakesNoWrite(t *testing.T) {
	w, _ := requestWriter(t)
	cutoff := time.Unix(1_800_000_000, 0)
	seedExpiredRequests(t, w.db, cutoff, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := w.Health().Writes
	if n, err := w.PruneRequests(ctx, cutoff); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("cancelled prune = %d, %v", n, err)
	}
	if writes := w.Health().Writes - before; writes != 0 {
		t.Fatalf("cancelled batch made %d writer calls", writes)
	}
	var count int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancelled batch retained %d rows: %v", count, err)
	}
}

func TestRequestExpiryFailedBatchRollsBack(t *testing.T) {
	w, _ := requestWriter(t)
	cutoff := time.Unix(1_800_000_000, 0)
	seedExpiredRequests(t, w.db, cutoff, 259)
	// Fail after the first deletion, so a failed batch must undo that deletion.
	if _, err := w.db.Exec(`CREATE TRIGGER fail_request_expiry BEFORE DELETE ON request_ids
WHEN (SELECT COUNT(*) FROM request_ids) < 259
BEGIN SELECT RAISE(ABORT, 'expiry blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PruneRequests(t.Context(), cutoff); err == nil {
		t.Fatal("failed batch returned success")
	}
	var count int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM request_ids").Scan(&count); err != nil || count != 259 {
		t.Fatalf("failed batch retained %d rows: %v; want 259", count, err)
	}
}
