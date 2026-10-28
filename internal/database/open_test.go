package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A relative data folder names another folder after a change of working
// folder, so Open must not reuse the handle it found by that name.
func TestOpenRelativeNameFollowsTheWorkingFolder(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Chdir(first)
	a, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(filepath.Join(first, "data")) })
	t.Chdir(second)
	b, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(filepath.Join(second, "data")) })
	if a == b {
		t.Fatal("Open(\"data\") returned the first folder's database after a change of working folder")
	}
	if _, err := os.Stat(filepath.Join(second, "data")); err != nil {
		t.Fatalf("second data folder: %v", err)
	}
	// An absolute name keeps returning its handle.
	if c, err := Open(filepath.Join(second, "data")); err != nil || c != b {
		t.Fatalf("absolute Open = %p, %v; want %p", c, err, b)
	}
}

// A store's first query in a process can run inside another store's
// transaction. When its tables are already current, Migrate must not wait
// for the writer that transaction holds.
func TestMigrateOfACurrentStoreDoesNotWaitForTheWriter(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	create := func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS t (x)")
		return err
	}
	if err := Migrate(db, "t.schema", 1, create); err != nil {
		t.Fatal(err)
	}
	// A new handle, as in a new process, has no record of the migration.
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	if db, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	done := make(chan error, 1)
	err = Immediate(context.Background(), db, func(*sql.Conn) error {
		go func() { done <- Migrate(db, "t.schema", 1, create) }()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("Migrate of a current store waited for the writer")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An older dyna must not migrate a store that a newer dyna wrote: it fails
// and leaves the store as it found it, on the same handle and on a new one.
func TestMigrateRefusesANewerSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	if err := Migrate(db, "t.schema", 2, func(*sql.Conn, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, fresh := range []bool{false, true} {
		if fresh {
			if err := Close(dir); err != nil {
				t.Fatal(err)
			}
			if db, err = Open(dir); err != nil {
				t.Fatal(err)
			}
		}
		ran := false
		err := Migrate(db, "t.schema", 1, func(*sql.Conn, int) error { ran = true; return nil })
		if !errors.Is(err, ErrNewerSchema) || ran {
			t.Fatalf("Migrate to 1 over 2 (new handle %v): err=%v, apply ran=%v; want ErrNewerSchema and no apply", fresh, err, ran)
		}
		var have string
		if err := db.QueryRow("SELECT value FROM meta WHERE key='t.schema'").Scan(&have); err != nil || have != "2" {
			t.Fatalf("stored version = %q, %v; want 2 unchanged", have, err)
		}
	}
}

func TestUninitializedStoreIsNotAnUpgrade(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	migrates.Store(false)
	t.Cleanup(func() { migrates.Store(true); _ = Close(dir) })
	err = Migrate(db, "shells.version", 6, func(*sql.Conn, int) error { t.Fatal("migration ran"); return nil })
	if err == nil || IsUpdatingSchema(err, "shells.version") {
		t.Fatalf("uninitialized schema error = %v; want an immediate failure", err)
	}
}

// Outside the engine, a store at an old version fails clearly and nothing is
// written: no migration, no new dyna.db and no meta table (requirement 41).
func TestOnlyTheEngineMigrates(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, "t.schema", 1, func(*sql.Conn, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	migrates.Store(false)
	t.Cleanup(func() { migrates.Store(true) })

	if db, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	ran := false
	err = Migrate(db, "t.schema", 2, func(*sql.Conn, int) error { ran = true; return nil })
	if !errors.Is(err, errNotMigrated) || ran {
		t.Fatalf("Migrate to 2 outside the engine: err=%v, apply ran=%v; want errNotMigrated and no apply", err, ran)
	}
	if !strings.HasPrefix(err.Error(), "Dyna is updating; retry in a minute") {
		t.Fatalf("older schema error = %q; want it to start with the updating notice", err)
	}
	if !strings.Contains(err.Error(), "t.schema is version 1, this dyna needs 2") {
		t.Fatalf("error %q does not name the store and both versions", err)
	}
	var have string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='t.schema'").Scan(&have); err != nil || have != "1" {
		t.Fatalf("stored version = %q, %v; want 1 unchanged", have, err)
	}
	// A current store still opens.
	if err := Migrate(db, "t.schema", 1, func(*sql.Conn, int) error { t.Fatal("apply ran"); return nil }); err != nil {
		t.Fatal(err)
	}

	empty := t.TempDir()
	if _, err := Open(empty); !errors.Is(err, errNotMigrated) {
		t.Fatalf("Open of a folder without dyna.db: %v; want errNotMigrated", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "dyna.db")); !os.IsNotExist(err) {
		t.Fatalf("Open outside the engine created dyna.db: %v", err)
	}
	missing := filepath.Join(empty, "missing")
	if _, err := Open(missing); !errors.Is(err, errNotMigrated) {
		t.Fatalf("Open of a missing folder: %v; want errNotMigrated", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("Open outside the engine created its folder: %v", err)
	}
}
