package engine

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

// Every store that calls database.Migrate must be in the shared store registry, or
// processes other than the engine would find its tables missing.
func TestEngineMigratesEveryStore(t *testing.T) {
	dir := t.TempDir()
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`database\.Migrate\(db, "([^"]+)"`)
	found := 0
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllSubmatch(source, -1) {
			found++
			var version string
			if err := db.QueryRow("SELECT value FROM meta WHERE key=?", string(m[1])).Scan(&version); err != nil {
				t.Errorf("%s migrates %s, but MigrateStores did not: %v", path, m[1], err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 15 {
		t.Fatalf("found %d database.Migrate calls under internal/, want at least 15; did the call pattern change?", found)
	}
}

// A read-only engine promises never to write, so it migrates nothing.
func TestReadOnlyEngineMigratesNothing(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if err := ownStores(dir, true); err != nil {
		t.Fatal(err)
	}
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM meta WHERE key != 'schema.version'").Scan(&keys); err != nil || keys != 0 {
		t.Fatalf("read-only engine wrote %d store versions (%v); want none", keys, err)
	}
}

// A failed migration leaves the engine running read-only, with the cause in
// its Health status, so it serves no writes and the install can report it.
func TestFailedMigrationStartsReadOnlyWithTheCause(t *testing.T) {
	dir := t.TempDir()
	saved := migrateStores
	t.Cleanup(func() { migrateStores = saved })
	migrateStores = func(string) error { return errors.New("migrate dyna.db: table things: no such column") }
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if err := database.Migrate(db, "t.schema", 1, func(*sql.Conn, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	keepSchemaOwner(t)
	readOnly, status := startStores(dir, false)
	if !readOnly || status != "read-only: migrate dyna.db: table things: no such column" {
		t.Fatalf("failed migration gave readOnly=%v status=%q", readOnly, status)
	}
	// The read-only engine no longer owns the schema, so a store that opens
	// later at an older version fails instead of migrating lazily.
	ran := false
	err = database.Migrate(db, "t.schema", 2, func(*sql.Conn, int) error { ran = true; return nil })
	if err == nil || ran || !strings.Contains(err.Error(), "only the engine creates and migrates dyna.db") {
		t.Fatalf("lazy migration after a failed start: err=%v, ran=%v; want refused and nothing written", err, ran)
	}
	var have string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='t.schema'").Scan(&have); err != nil || have != "1" {
		t.Fatalf("stored version = %q, %v; want 1 unchanged", have, err)
	}
	database.OwnSchema() // as an engine whose migrations succeed
	migrateStores = func(string) error { return nil }
	if readOnly, status := startStores(dir, false); readOnly || status != "" {
		t.Fatalf("good migration gave readOnly=%v status=%q", readOnly, status)
	}
}

// keepSchemaOwner restores this process's schema ownership after the test,
// whatever it was before, since startStores may give it up.
func keepSchemaOwner(t *testing.T) {
	owned := database.OwnsSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		} else {
			database.DisownSchema()
		}
	})
}
