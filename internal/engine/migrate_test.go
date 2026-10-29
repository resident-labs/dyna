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
	"dyna-agent/internal/fsutil"
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
	if readOnly, status, _ := startStores(dir, false, ""); readOnly {
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
	if err := ownStores(dir, true, dir); err != nil {
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
	readOnly, status, _ := startStores(dir, false, "")
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
	if readOnly, status, _ := startStores(dir, false, ""); readOnly || status != "" {
		t.Fatalf("good migration gave readOnly=%v status=%q", readOnly, status)
	}
}

// A development executable cannot migrate the owner's default data folder.
// The installed path, a custom folder, and the explicit owner recovery switch
// keep upgrades, tests, and hand recovery possible.
func TestLiveMigrationRequiresInstalledExecutable(t *testing.T) {
	exe, err := fsutil.InstalledExecutable()
	if err != nil {
		t.Fatal(err)
	}
	newPaths := func(t *testing.T) (ownerData, installed string) {
		root := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
		home := filepath.Join(root, "home")
		return filepath.Join(home, ".local", "share", "dyna"), filepath.Join(home, ".local", "bin", "dyna")
	}

	t.Run("refuses before creating the database", func(t *testing.T) {
		keepSchemaOwner(t)
		ownerData, _ := newPaths(t)
		readOnly, status, _, cause := startMode(ownerData, false, "", ownerData)
		t.Cleanup(func() { database.Close(ownerData) })
		if _, err := os.Stat(filepath.Join(ownerData, "dyna.db")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uninstalled executable created dyna.db: %v", err)
		}
		if !readOnly || cause == nil || !strings.Contains(status, "only the installed engine") || !strings.Contains(status, "DYNA_ALLOW_DEV_MIGRATION=1") {
			t.Fatalf("uninstalled live migration: readOnly=%v status=%q cause=%v", readOnly, status, cause)
		}
	})

	t.Run("refuses a symlink to a missing default folder", func(t *testing.T) {
		keepSchemaOwner(t)
		ownerData, _ := newPaths(t)
		alias := filepath.Join(t.TempDir(), "live")
		if err := os.Symlink(ownerData, alias); err != nil {
			t.Fatal(err)
		}
		readOnly, status, _, cause := startMode(alias, false, "", ownerData)
		t.Cleanup(func() { database.Close(alias) })
		if _, err := os.Stat(filepath.Join(ownerData, "dyna.db")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("symlinked uninstalled executable created dyna.db: %v", err)
		}
		if !readOnly || cause == nil || !strings.Contains(status, "only the installed engine") {
			t.Fatalf("symlinked live migration: readOnly=%v status=%q cause=%v; want an identity refusal", readOnly, status, cause)
		}
	})

	t.Run("accepts the symlinked installed executable", func(t *testing.T) {
		keepSchemaOwner(t)
		ownerData, installed := newPaths(t)
		if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, installed); err != nil {
			t.Fatal(err)
		}
		readOnly, status, _, cause := startMode(ownerData, false, "", ownerData)
		t.Cleanup(func() { database.Close(ownerData) })
		if readOnly || cause != nil {
			t.Fatalf("installed live migration: readOnly=%v status=%q cause=%v", readOnly, status, cause)
		}
	})

	t.Run("accepts a configured custom install directory", func(t *testing.T) {
		keepSchemaOwner(t)
		ownerData, _ := newPaths(t)
		installDir := filepath.Join(t.TempDir(), "opt", "dyna")
		if err := os.MkdirAll(installDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, filepath.Join(installDir, "dyna")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DYNA_INSTALL_DIR", installDir)
		readOnly, status, _, cause := startMode(ownerData, false, "", ownerData)
		t.Cleanup(func() { database.Close(ownerData) })
		if readOnly || cause != nil {
			t.Fatalf("custom installed live migration: readOnly=%v status=%q cause=%v", readOnly, status, cause)
		}
	})

	t.Run("preserves custom folders and owner recovery", func(t *testing.T) {
		for _, recovery := range []bool{false, true} {
			keepSchemaOwner(t)
			ownerData, _ := newPaths(t)
			dataDir := filepath.Join(t.TempDir(), "scratch")
			if recovery {
				dataDir = ownerData
				t.Setenv("DYNA_ALLOW_DEV_MIGRATION", "1")
			}
			readOnly, status, _, cause := startMode(dataDir, false, "", ownerData)
			t.Cleanup(func() { database.Close(dataDir) })
			if readOnly || cause != nil {
				t.Fatalf("recovery=%v: readOnly=%v status=%q cause=%v", recovery, readOnly, status, cause)
			}
		}
	})
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

// The fake clock covers a live blocker, one immediate alert, and recovery after
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
	readOnly, status, _ := startStores(dir, false, "")
	if !readOnly {
		t.Fatal("live old writer did not block migration")
	}
	clock := newFakeClock()
	set := clock.timers(t.Context())
	defer set.close()
	alerts := 0
	var woke []string
	recoverStores(set, fmt.Errorf("migrate dyna.db: %w", blocker), func(int) (callerid.Process, error) { return live, nil }, func() {
		readOnly, status, _ = startStores(dir, false, "")
	}, func(cause error) {
		alerts++
		migrationAlert(rooms, cause, func(roomID string) { woke = append(woke, roomID) })
	})
	if !readOnly || attempts != 1 || alerts != 1 || len(woke) != 1 || woke[0] != rec.ID {
		t.Fatalf("immediate alert: readOnly=%v attempts=%d alerts=%d woke=%v; want true, 1, 1, root wake", readOnly, attempts, alerts, woke)
	}
	clock.advance(3 * time.Minute)
	if alerts != 1 {
		t.Fatalf("immediate alert repeated after 3 minutes: %d", alerts)
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
