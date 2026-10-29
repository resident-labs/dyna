package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// The version guard fails closed: a check that cannot run refuses the write.
// A transaction context can expire after BEGIN, while the update callback
// keeps its own context (shells.Save) and COMMIT runs without cancellation,
// so an unreadable check must not let the update commit to a store it never
// verified (PR #962 review, D-468).
func TestRefuseAdvancedStoreFailsClosedWhenItsCheckCannotRun(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, "guard.key", 1, func(*sql.Conn, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	DisownSchema()
	t.Cleanup(OwnSchema)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = Immediate(context.Background(), db, func(conn *sql.Conn) error {
		cancel()
		err := refuseAdvancedStore(ctx, conn, db)
		if err == nil {
			t.Fatal("a canceled version check let the write proceed")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refusal lost the check's cause: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
