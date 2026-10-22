package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// A migration that changes an existing store first copies dyna.db into the
// pre-migration folder, and the copy holds the store as it was. A second
// migration on the same handle shares that copy (requirement 23).
func TestMigrationBacksUpFirst(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	preMigration := filepath.Join(dir, "backups", PreMigrationFolder)
	for _, key := range []string{"a.schema", "b.schema"} {
		if err := Migrate(db, key, 1, func(*sql.Conn, int) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE notes(body TEXT); INSERT INTO notes VALUES ('kept')"); err != nil {
		t.Fatal(err)
	}
	if names, _ := backups(filepath.Join(dir, "backups")); len(names) != 0 {
		t.Fatalf("new stores took daily backups %v; want none", names)
	}
	if names, _ := backups(preMigration); len(names) != 0 {
		t.Fatalf("new stores took pre-migration copies %v; want none", names)
	}
	drop := func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS notes")
		return err
	}
	for _, key := range []string{"a.schema", "b.schema"} {
		if err := Migrate(db, key, 2, drop); err != nil {
			t.Fatal(err)
		}
	}
	names, err := backups(preMigration)
	if err != nil || len(names) != 1 {
		t.Fatalf("pre-migration copies after two migrations = %v, %v; want one", names, err)
	}
	if info, err := os.Stat(names[0]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode = %v, %v; want 0600 like dyna.db", info.Mode().Perm(), err)
	}
	saved, err := sql.Open("sqlite", names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	var version, body string
	if err := saved.QueryRow("SELECT value FROM meta WHERE key='a.schema'").Scan(&version); err != nil || version != "1" {
		t.Fatalf("backup a.schema = %q, %v; want 1", version, err)
	}
	if err := saved.QueryRow("SELECT body FROM notes").Scan(&body); err != nil || body != "kept" {
		t.Fatalf("backup notes = %q, %v; want the row from before the migration", body, err)
	}
}

// Only the newest three backups stay, and an interrupted backup's .partial
// file goes.
func TestBackupKeepsTheNewestThree(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	backupNow = func() time.Time { return clock }
	t.Cleanup(func() { backupNow = time.Now })
	backupsDir := filepath.Join(dir, "backups")
	var made []string
	for range 5 {
		name, err := backup(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, name)
		clock = clock.Add(time.Hour)
	}
	partial := filepath.Join(backupsDir, "dyna-20261019T000000.000000000Z.db.partial")
	if err := os.WriteFile(partial, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	names, err := backups(backupsDir)
	if err != nil || len(names) != 3 || names[0] != made[3] || names[1] != made[4] {
		t.Fatalf("backups = %v, %v; want the newest three, starting with %s", names, err, made[3])
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("partial backup still there: %v", err)
	}
}

// A caller that arrives while the pre-migration copy runs waits for it: it must
// not pass the gate and migrate on a copy that never finished.
func TestSecondCallerWaitsForTheRunningPreMigrationBackup(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	// A file where the backups folder belongs fails every copy, so a caller that
	// does not wait for one is visible.
	if err := os.WriteFile(filepath.Join(dir, "backups"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	started, waiting := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var startOnce, waitOnce, releaseOnce sync.Once
	backupStarted = func() { startOnce.Do(func() { close(started) }); <-release }
	t.Cleanup(func() { backupStarted = func() {} })
	backupWaiting = func() { waitOnce.Do(func() { close(waiting) }) }
	t.Cleanup(func() { backupWaiting = func() {} })
	first := make(chan error, 1)
	go func() { first <- BackUpOnce(db) }()
	<-started
	second := make(chan error, 1)
	go func() { second <- BackUpOnce(db) }()
	select {
	case <-waiting:
	case <-time.After(30 * time.Second):
		releaseOnce.Do(func() { close(release) })
		t.Fatal("the second caller did not wait for the running backup")
	}
	releaseOnce.Do(func() { close(release) })
	firstErr, secondErr := <-first, <-second
	if firstErr == nil || secondErr == nil {
		t.Fatalf("errors = %v and %v; a copy failed, so no caller may pass", firstErr, secondErr)
	}
	if secondErr != firstErr {
		t.Fatalf("the second caller got %v; want the running copy's result %v", secondErr, firstErr)
	}
}

// The pre-migration folder keeps its own newest three, the number requirement
// 23 keeps of a backup history, and a migration that fails at every start
// cannot prune the daily history.
func TestPreMigrationCopiesKeepTheirOwnHistory(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clock := base
	backupNow = func() time.Time { return clock }
	t.Cleanup(func() { backupNow = time.Now })
	daily := filepath.Join(dir, "backups")
	preMigration := filepath.Join(daily, PreMigrationFolder)
	copyName := func(dir string, at time.Time) string {
		return filepath.Join(dir, "dyna-"+at.UTC().Format(backupStamp)+".db")
	}
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The engine's daily history: the newest three of five copies.
	var madeDaily []string
	for i := range 5 {
		clock = base.AddDate(0, 0, i-5)
		if _, err := backup(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		madeDaily = append(madeDaily, copyName(daily, clock))
	}
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		clock = base.Add(time.Duration(i) * time.Minute)
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := BackUpOnce(db); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if err := Close(dir); err != nil {
			t.Fatal(err)
		}
	}
	// The third-newest pre-migration copy stays; the two older ones go.
	want := []string{
		copyName(preMigration, base.Add(3*time.Minute)),
		copyName(preMigration, base.Add(4*time.Minute)),
		copyName(preMigration, base.Add(5*time.Minute)),
	}
	if names, err := backups(preMigration); err != nil || len(names) != keepPreMigration || !slices.Equal(names, want) {
		t.Fatalf("pre-migration copies = %v, %v; want %v", names, err, want)
	}
	// The failing starts pruned none of the daily history.
	if names, err := backups(daily); err != nil || !slices.Equal(names, madeDaily[len(madeDaily)-keepBackups:]) {
		t.Fatalf("daily backups = %v, %v; want the newest %d", names, err, keepBackups)
	}
}

// Every folder a copy relies on is synced on every call, in order: the data
// folder and backups/ before the copy, and the copy's own folder after the
// rename. A retry after a failed sync finds the folders already there, so it
// must sync their entries again rather than skip them.
func TestPreMigrationCopySyncsItsFolders(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var synced []string
	real := syncDir
	syncDir = func(path string) error {
		synced = append(synced, path)
		return real(path)
	}
	t.Cleanup(func() { syncDir = real })
	backupsDir, err := backupDir(db)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Dir(backupsDir), backupsDir, filepath.Join(backupsDir, PreMigrationFolder)}
	if err := BackUpOnce(db); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(synced, want) {
		t.Fatalf("synced = %v; want the data folder, backups/ and the copy's own folder %v", synced, want)
	}
	// A retry on a new handle with the folders already there syncs them again.
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	synced = nil
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	if err := BackUpOnce(db); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(synced, want) {
		t.Fatalf("synced on a retry = %v; want the same folders %v", synced, want)
	}
}

// Each of the three syncs is required: an error comes back to the caller
// instead of a copy that only looks complete.
func TestPreMigrationCopyReturnsEverySyncFailure(t *testing.T) {
	for name, fail := range map[string]int{
		"data folder":    0,
		"backups folder": 1,
		"copy folder":    2,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { Close(dir) })
			synced := 0
			real := syncDir
			syncDir = func(path string) error {
				defer func() { synced++ }()
				if synced == fail {
					return errors.New("sync failed")
				}
				return real(path)
			}
			t.Cleanup(func() { syncDir = real })
			if err := BackUpOnce(db); err == nil {
				t.Fatalf("a failing %s sync was swallowed", name)
			}
		})
	}
}

// A failed parent sync is not skipped by the retry that finds the folders
// already there: the retry syncs the data folder and backups/ again, in order,
// before it succeeds.
func TestPreMigrationCopyRetrySyncsTheParentsAgain(t *testing.T) {
	for name, fail := range map[string]int{"data folder": 0, "backups folder": 1} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { Close(dir) })
			backupsDir, err := backupDir(db)
			if err != nil {
				t.Fatal(err)
			}
			real := syncDir
			t.Cleanup(func() { syncDir = real })
			calls := 0
			syncDir = func(path string) error {
				defer func() { calls++ }()
				if calls == fail {
					return errors.New("sync failed")
				}
				return real(path)
			}
			if err := BackUpOnce(db); err == nil {
				t.Fatalf("the first copy succeeded although the %s sync failed", name)
			}
			// Both folders are there now, which is what made the retry skip them.
			for _, p := range []string{filepath.Dir(backupsDir), backupsDir} {
				if _, err := os.Stat(p); err != nil {
					t.Fatalf("the failed copy left %s missing: %v", p, err)
				}
			}
			var synced []string
			syncDir = func(path string) error {
				synced = append(synced, path)
				return real(path)
			}
			if err := BackUpOnce(db); err != nil {
				t.Fatalf("the retry failed: %v", err)
			}
			want := []string{filepath.Dir(backupsDir), backupsDir, filepath.Join(backupsDir, PreMigrationFolder)}
			if !slices.Equal(synced, want) {
				t.Fatalf("synced on the retry = %v; want the parents and the copy's folder %v", synced, want)
			}
			if names, err := backups(filepath.Join(backupsDir, PreMigrationFolder)); err != nil || len(names) != 1 {
				t.Fatalf("copies after the retry = %v, %v; want one", names, err)
			}
		})
	}
}

// A start copies the data it sees even when the stored versions do not change,
// so the copy a restore would use holds the newest canonical rows.
func TestPreMigrationCopyHoldsACanonicalChange(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	backupNow = func() time.Time { return clock }
	t.Cleanup(func() { backupNow = time.Now })
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE notes(body TEXT)"); err != nil {
		t.Fatal(err)
	}
	write := func(value string) {
		t.Helper()
		if err := Immediate(context.Background(), db, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(context.Background(), "INSERT INTO notes(body) VALUES (?)", value)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	if err := BackUpOnce(db); err != nil {
		t.Fatal(err)
	}
	// A canonical row changes and no meta row does.
	write("second")
	clock = clock.Add(time.Minute)
	if err := Close(dir); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(dir)
	if err := BackUpOnce(db); err != nil {
		t.Fatal(err)
	}
	names, err := backups(filepath.Join(dir, "backups", PreMigrationFolder))
	if err != nil || len(names) != 2 {
		t.Fatalf("pre-migration copies = %v, %v; want two", names, err)
	}
	saved, err := sql.Open("sqlite", names[len(names)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	var body string
	if err := saved.QueryRow("SELECT body FROM notes ORDER BY rowid DESC LIMIT 1").Scan(&body); err != nil || body != "second" {
		t.Fatalf("newest copy holds %q, %v; want the row written before this start", body, err)
	}
}

// The daily backup runs when the newest backup is a day old, and otherwise
// says how long to wait.
func TestDailyBackupWaitsADay(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { Close(dir) })
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	backupNow = func() time.Time { return clock }
	t.Cleanup(func() { backupNow = time.Now })
	count := func() int {
		names, err := backups(filepath.Join(dir, "backups"))
		if err != nil {
			t.Fatal(err)
		}
		return len(names)
	}
	for _, step := range []struct {
		after   time.Duration
		wait    time.Duration
		backups int
	}{{0, 24 * time.Hour, 1}, {time.Hour, 23 * time.Hour, 1}, {23 * time.Hour, 24 * time.Hour, 2}} {
		clock = clock.Add(step.after)
		wait, err := DailyBackup(context.Background(), dir)
		if err != nil || wait != step.wait || count() != step.backups {
			t.Fatalf("at +%s: wait %s, %d backups, %v; want wait %s and %d backups", step.after, wait, count(), err, step.wait, step.backups)
		}
	}
}
