package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// resetCallers clears the caller table and turns caller detail off, so tests stay independent.
func resetCallers(t *testing.T) {
	t.Helper()
	callers.Lock()
	callers.byName = nil
	callers.Unlock()
	SetCallerDetail(false)
	t.Cleanup(func() {
		SetCallerDetail(false)
		callers.Lock()
		callers.byName = nil
		callers.Unlock()
	})
}

func TestCallerDetailCountsWriterTransactionsAndCapsNames(t *testing.T) {
	resetCallers(t)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dyna.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer writerLocks.Delete(db)
	if _, err := db.Exec("CREATE TABLE counter(value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	SetCallerDetail(true)
	ctx := context.Background()
	if err := Immediate(ctx, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "INSERT INTO counter VALUES(1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("update failed")
	if err := Immediate(ctx, db, func(*sql.Conn) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("rollback error = %v", err)
	}
	var total uint64
	for _, c := range Measurements().Callers {
		total += c.Transactions
	}
	if total != 2 {
		t.Fatalf("writer transactions by caller = %d, want 2 (commit and rollback both count)", total)
	}

	resetCallers(t)
	SetCallerDetail(true)
	for i := range maxCallers + 5 {
		recordCaller(fmt.Sprintf("caller%d", i), time.Millisecond)
	}
	var other CallerCount
	for _, c := range callerSnapshot() {
		if c.Caller == "other" {
			other = c
		}
	}
	if len(callerSnapshot()) > maxCallers+1 || other.Transactions != 5 {
		t.Fatalf("caller table has %d names, other = %+v; want at most %d names and 5 overflow transactions",
			len(callerSnapshot()), other, maxCallers+1)
	}
}

// A goals store read is named with the caller outside internal/goals, so the
// barrier shows which lifecycle check asked for it.
func TestWriterLabelNamesGoalsBarrierByItsUpstreamCaller(t *testing.T) {
	const (
		tx       = "dyna-agent/internal/database.transact"
		write    = "dyna-agent/internal/state.(*Writer).Write"
		list     = "dyna-agent/internal/goals.(*Store).list"
		openHeld = "dyna-agent/internal/goals.(*Store).OpenHeldBy"
		closed   = "dyna-agent/internal/worktrees.(*Store).SessionClosed"
	)
	tests := []struct {
		name  string
		stack []string
		want  string
	}{
		{"goals barrier names its upstream caller", []string{tx, write, list, openHeld, closed}, list + " via " + closed},
		{"other store keeps its own name", []string{tx, write, "dyna-agent/internal/worktrees.(*Store).Save"}, "dyna-agent/internal/worktrees.(*Store).Save"},
		{"goalsvc is not the goals store", []string{tx, "dyna-agent/internal/goalsvc.(*Service).Note"}, "dyna-agent/internal/goalsvc.(*Service).Note"},
		{"goals with no outside caller names itself", []string{tx, list}, list},
		{"empty stack is unknown", []string{tx, write}, "unknown"},
	}
	for _, tc := range tests {
		if got := writerLabel(tc.stack); got != tc.want {
			t.Errorf("%s: writerLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}
