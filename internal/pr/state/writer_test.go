package state

import (
	"context"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"

	appstate "dyna-agent/internal/state"
)

// The PR store's transactions go through the process's one writer, so the
// engine counts them in Health and orders them with every other write. Reads
// never write: both reads use a read snapshot. ReadLocked's caller holds the
// store's lock, under which every PR write commits, so it needs no barrier.
func TestPRStoreTransactionsGoThroughTheWriter(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	db, err := open(root)
	if err != nil {
		t.Fatal(err)
	}
	writes := func() int { return appstate.For(db).Health().Writes }
	for _, tc := range []struct {
		name string
		run  func() error
		want int
	}{
		{"TakePruned", func() error { _, err := TakePruned(root); return err }, 1},
		{"ReadLocked, under the store's lock", func() error { _, err := ReadLocked(root); return err }, 0},
		{"Read, a snapshot", func() error { _, err := Read(root); return err }, 0},
	} {
		before := writes()
		if err := tc.run(); err != nil {
			t.Fatal(err)
		}
		if got := writes() - before; got != tc.want {
			t.Errorf("%s counted %d writes, want %d", tc.name, got, tc.want)
		}
	}
}

// The PR view polls the revision, so it must never go back, and a write
// that only deletes a record must move it too.
func TestDeleteOnlyWriteMovesTheRevision(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	revision := func() int {
		rev, err := Revision(root)
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(rev)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := revision()
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	delete(st.Records, "github.com/acme/widgets#1")
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if after := revision(); after <= before {
		t.Fatalf("revision %d -> %d after a delete; want it to move forward", before, after)
	}
}

// The engine takes the PR store's lock on calls, so a wait for it ends with
// the call's context instead of blocking the call forever.
func TestUpdateWaitsForTheLockWithinItsContext(t *testing.T) {
	root := newRoot(t)
	held, err := os.OpenFile(LockPath(root), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Update(ctx, root, func(*State) error { t.Fatal("Update ran under a lock it never took"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Update with a held lock and an ended context = %v, want context.Canceled", err)
	}
}
