package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
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

// An engine start creates no profile: every profile is one the owner added.
func TestEngineStartCreatesNoProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	keepSchemaOwner(t)
	if readOnly, status, _ := startStores(dir, false); readOnly {
		t.Fatalf("start = read-only: %s", status)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := os.Stat(profile.DefaultPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("engine start wrote %s: %v", profile.DefaultPath(), err)
	}
	store, err := profile.Load(profile.DefaultPath())
	if err != nil || len(store.Profiles) != 0 {
		t.Fatalf("profiles after engine start = %+v, %v", store, err)
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
	readOnly, status, _ := startStores(dir, false)
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
	if readOnly, status, _ := startStores(dir, false); readOnly || status != "" {
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

// The fake clock covers a live blocker, one delayed alert, and recovery after
// PID reuse. Migration must not run while the old writer still owns that PID.
func TestMigrationRecoversAfterOldWriterExits(t *testing.T) {
	keepSchemaOwner(t)
	database.OwnSchema()
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	if err := room.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: filepath.Join(dir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "dyna", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	saved := migrateStores
	t.Cleanup(func() { migrateStores = saved })
	writer := callerid.Process{PID: 7, Start: 1}
	live := writer
	blocker := &subagent.LivePromptWriterError{Process: writer, Reason: "live turn pid 7 uses format 2; need 3"}
	attempts := 0
	migrateStores = func(string) error {
		attempts++
		if live == writer {
			return blocker
		}
		return nil
	}
	readOnly, status, _ := startStores(dir, false)
	if !readOnly {
		t.Fatal("live old writer did not block migration")
	}
	clock := newFakeClock()
	set := clock.timers(t.Context())
	defer set.close()
	alerts := 0
	recoverStores(set, fmt.Errorf("migrate dyna.db: %w", blocker), func(int) (callerid.Process, error) { return live, nil }, func() {
		readOnly, status, _ = startStores(dir, false)
	}, func(cause error) {
		alerts++
		migrationAlert(rooms, cause)
	})
	clock.advance(3 * time.Minute)
	if !readOnly || attempts != 1 || alerts != 1 {
		t.Fatalf("blocked: readOnly=%v attempts=%d alerts=%d; want true, 1, 1", readOnly, attempts, alerts)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 {
		t.Fatalf("root alerts = %v, %v; want one", posts, err)
	}
	if decision, _ := room.RootContext(posts[0], rec.Name); decision != room.RootWake || !strings.Contains(posts[0].Text, blocker.Reason) {
		t.Fatalf("alert must wake root and include the cause: %+v", posts[0])
	}
	// The card shows the remedy as a command chip, not a block of prose.
	payload, ok := posts[0].Alert()
	if !ok || payload.Source != room.AlertEngine || payload.Remedy != "systemctl --user restart dyna-engine.service" {
		t.Fatalf("alert payload = %+v, %v", payload, ok)
	}
	live.Start++ // A new process reused the PID after the old writer exited.
	clock.advance(15 * time.Second)
	if readOnly || status != "" || attempts != 2 {
		t.Fatalf("after exit: readOnly=%v status=%q attempts=%d; want false, empty, 2", readOnly, status, attempts)
	}
	clock.advance(3 * time.Minute)
	if alerts != 1 || attempts != 2 {
		t.Fatalf("recovery repeated: alerts=%d attempts=%d", alerts, attempts)
	}
}
