package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/fsutil"
)

// keepBackups is how many backups of dyna.db stay in <data>/backups.
const keepBackups = 3

// keepPreMigration is how many copies taken before a migration stay in
// <data>/backups/pre-migration. Requirement 23 keeps the newest three of a
// backup history, and these copies are one.
const keepPreMigration = 3

// PreMigrationFolder holds the copies taken before a migration, apart from the
// daily history, so a migration that fails at every start cannot prune it.
const PreMigrationFolder = "pre-migration"

// backupDay is how often the engine backs up dyna.db when nothing migrates.
const backupDay = 24 * time.Hour

// backupStamp names a backup by its UTC time, so names sort by age.
const backupStamp = "20060102T150405.000000000Z"

// backupNow lets tests drive the backup clock.
var backupNow = time.Now

// backupMu lets one backup at a time write and prune <data>/backups.
var backupMu sync.Mutex

// backupStarted runs when a copy begins, so a test can hold one open.
var backupStarted = func() {}

// backupWaiting runs before a caller waits for a copy another caller started,
// so a test can see that a caller waits instead of passing the gate.
var backupWaiting = func() {}

// backedUp records the backup that the migrations of one engine start share.
// It holds one completion per handle: a caller that arrives while the copy
// runs waits for it, so no store migrates on the strength of a copy that
// never finished.
var backedUp sync.Map // *sql.DB -> *backupCompletion

type backupCompletion struct {
	done chan struct{}
	err  error
}

// BackUpOnce takes the backup that the migrations of one engine start share
// (requirement 23): the first call backs db up, and a caller that arrives
// while it runs waits for its result. A store that imports old files into a
// new table calls it first, because Migrate backs up only before it changes
// an existing table.
func BackUpOnce(db *sql.DB) error {
	fresh := &backupCompletion{done: make(chan struct{})}
	loaded, running := backedUp.LoadOrStore(db, fresh)
	completion := loaded.(*backupCompletion)
	if running {
		backupWaiting()
		<-completion.done
		return completion.err
	}
	completion.err = preMigrationBackup(db)
	close(completion.done)
	if completion.err != nil {
		// A later caller tries again instead of sharing the failure.
		backedUp.Delete(db)
	}
	return completion.err
}

// preMigrationBackup copies dyna.db into its own folder before a migration, so
// the copy a restore would use always holds the data this start saw, and the
// daily history keeps the newest few of its own.
func preMigrationBackup(db *sql.DB) error {
	dir, err := backupDir(db)
	if err != nil {
		return err
	}
	_, err = copyDatabase(context.Background(), db, filepath.Join(dir, PreMigrationFolder), keepPreMigration, filepath.Dir(dir), dir)
	return err
}

// backup writes an online copy of dyna.db to <data>/backups and keeps the
// newest keepBackups.
func backup(ctx context.Context, db *sql.DB) (string, error) {
	dir, err := backupDir(db)
	if err != nil {
		return "", err
	}
	return copyDatabase(ctx, db, dir, keepBackups, filepath.Dir(dir))
}

// copyDatabase writes an online copy of db to dir with VACUUM INTO and keeps
// the newest keep. VACUUM INTO reads one snapshot, so writers go on while it
// runs (requirement 23). It returns the new copy's path. parents are the
// folders that hold the entry naming each folder this copy relies on,
// shallowest first: they are synced on every call, because a retry after a
// failed sync finds the folders already there and must make their entries
// durable again.
func copyDatabase(ctx context.Context, db *sql.DB, dir string, keep int, parents ...string) (string, error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	backupStarted()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for _, parent := range parents {
		if err := syncDir(parent); err != nil {
			return "", err
		}
	}
	name := filepath.Join(dir, "dyna-"+backupNow().UTC().Format(backupStamp)+".db")
	// A crash leaves only a .partial file, which never counts as a backup.
	partial := name + ".partial"
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		os.Remove(partial)
		return "", err
	}
	if err := markCopy(ctx, partial); err != nil {
		os.Remove(partial)
		return "", err
	}
	// VACUUM INTO creates the file with the umask; dyna.db is private.
	if err := os.Chmod(partial, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(partial, name); err != nil {
		return "", err
	}
	// Sync the folder, so the new name survives a power cut.
	if err := syncDir(dir); err != nil {
		return "", err
	}
	return name, prune(dir, keep)
}

// syncDir syncs one folder. Tests replace it to record which folders a copy
// made durable, and in which order. The work is fsutil.SyncDir, the shared
// durable-rename step, so this package keeps no copy of it.
var syncDir = fsutil.SyncDir

func backupDir(db *sql.DB) (string, error) {
	var file string
	if err := db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(file), "backups"), nil
}

// backups lists the finished backups in dir, oldest first.
func backups(dir string) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "dyna-*.db"))
	sort.Strings(names)
	return names, err
}

// prune removes all but the newest keep, and any .partial file that an
// interrupted backup left.
func prune(dir string, keep int) error {
	names, err := backups(dir)
	if err != nil {
		return err
	}
	partials, err := filepath.Glob(filepath.Join(dir, "dyna-*.db.partial"))
	if err != nil {
		return err
	}
	old := partials
	if len(names) > keep {
		old = append(old, names[:len(names)-keep]...)
	}
	for _, name := range old {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// DailyBackup backs up dataDir's dyna.db when its newest backup is a day old
// or missing. It returns how long until the next one is due. Only the engine
// calls it.
func DailyBackup(ctx context.Context, dataDir string) (time.Duration, error) {
	db, err := Open(dataDir)
	if err != nil {
		return 0, err
	}
	dir, err := backupDir(db)
	if err != nil {
		return 0, err
	}
	names, err := backups(dir)
	if err != nil {
		return 0, err
	}
	if len(names) > 0 {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(names[len(names)-1]), "dyna-"), ".db")
		if newest, err := time.Parse(backupStamp, stamp); err == nil {
			if wait := newest.Add(backupDay).Sub(backupNow()); wait > 0 {
				return wait, nil
			}
		}
	}
	if _, err := backup(ctx, db); err != nil {
		return 0, err
	}
	return backupDay, nil
}
