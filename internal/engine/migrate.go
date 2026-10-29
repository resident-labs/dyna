package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

const (
	installMarker        = ".dyna-installed"
	installMarkerContent = "installed by install.sh\n"
)

// migrateStores lets startup tests inject a failing migration. Production and
// the fake engine both run the shared ordered registry.
var migrateStores = stores.Migrate

// installedExecutable lets startup tests simulate a direct launch.
var installedExecutable = fsutil.InstalledExecutable

// ownStores makes this engine the one process that migrates dyna.db, and
// migrates every store. A read-only engine never writes, so it does neither
// and fails on an old store like any other process.
func ownStores(dataDir string, readOnly bool, ownerDataDir string) error {
	if readOnly {
		return nil
	}
	if err := checkLiveMigration(dataDir, ownerDataDir); err != nil {
		return err
	}
	if err := profile.Migrate(profile.DefaultPath()); err != nil {
		return err
	}
	database.OwnSchema()
	return MigrateStores(dataDir)
}

// checkLiveMigration keeps branch and development binaries from advancing the
// owner's live schema. Custom data folders remain available to every build.
func checkLiveMigration(dataDir, ownerDataDir string) error {
	if ownerDataDir == "" || !sameFolder(dataDir, ownerDataDir) || os.Getenv("DYNA_ALLOW_DEV_MIGRATION") == "1" {
		return nil
	}
	exe, err := installedExecutable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fmt.Errorf("migrate dyna.db: resolve the running executable for the default data directory: %w; for owner-directed hand recovery set DYNA_ALLOW_DEV_MIGRATION=1", err)
	}
	// OwnerDataDir is DataDirForHome(home): ~/.local/share/dyna. The source
	// installer uses ~/.local/bin; the WSL and managed images use /usr/local/bin.
	home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(ownerDataDir))))
	installed := []string{filepath.Join(home, ".local", "bin", "dyna"), "/usr/local/bin/dyna"}
	for _, path := range installed {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil && resolved == exe {
			return nil
		}
	}
	// install.sh leaves a marker beside custom installations, so a direct
	// launch does not depend on the installer's temporary environment.
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(exe), installMarker))
	if err == nil && string(marker) == installMarkerContent {
		return nil
	}
	return fmt.Errorf("migrate dyna.db: only the installed engine may migrate the default data directory %s; running executable is %s, not %s or %s, and has no install.sh marker; for owner-directed hand recovery set DYNA_ALLOW_DEV_MIGRATION=1", ownerDataDir, exe, installed[0], installed[1])
}

func sameFolder(a, b string) bool {
	absoluteA, errA := filepath.Abs(a)
	absoluteB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	resolvedA, errA := resolveFolder(absoluteA)
	resolvedB, errB := resolveFolder(absoluteB)
	if errA == nil && errB == nil {
		return resolvedA == resolvedB
	}
	return filepath.Clean(absoluteA) == filepath.Clean(absoluteB)
}

// resolveFolder also resolves a symlink whose target folder does not exist
// yet. EvalSymlinks alone would treat that alias as a custom data folder.
func resolveFolder(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !os.IsNotExist(err) {
		return resolved, err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolvedParent, err := resolveFolder(parent)
	if err != nil {
		return "", err
	}
	resolved = filepath.Join(resolvedParent, filepath.Base(path))
	info, err := os.Lstat(resolved)
	if os.IsNotExist(err) {
		return resolved, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return resolved, nil
	}
	target, err := os.Readlink(resolved)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedParent, target)
	}
	return resolveFolder(target)
}

// startStores migrates dyna.db at engine start. A failed migration does not
// stop the engine: it runs read-only, so it serves no writes, and the
// returned status (Health's status) tells the install why (requirement 32).
func startStores(dataDir string, readOnly bool, ownerDataDir string) (stayReadOnly bool, status string, cause error) {
	if err := ownStores(dataDir, readOnly, ownerDataDir); err != nil {
		database.DisownSchema()
		log.Printf("dyna engine: %v; running read-only", err)
		return true, "read-only: " + err.Error(), err
	}
	return readOnly, "", nil
}

// startMode decides how a starting engine runs. A failed migration makes it
// read-only, with the cause as status, but it is still the owner's engine,
// so it keeps Connect: the owner needs remote access to see why. Connect is
// decided on the read-only flag the engine was started with (a second, QA
// engine), not on the read-only state a failure forced.
func startMode(dataDir string, askedReadOnly bool, group, ownerDataDir string) (readOnly bool, status, connectRefused string, cause error) {
	readOnly, status, cause = startStores(dataDir, askedReadOnly, ownerDataDir)
	return readOnly, status, connectRefusal(askedReadOnly, group, dataDir, ownerDataDir), cause
}

// MigrateStores runs every store migration on dataDir's dyna.db.
func MigrateStores(dataDir string) error { return migrateStores(dataDir) }

// dailyBackups backs up dyna.db once a day while the engine runs
// (requirement 23). It stops with ctx.
func dailyBackups(ctx context.Context, dataDir string) {
	for {
		wait, err := database.DailyBackup(ctx, dataDir)
		if err != nil {
			log.Printf("daily dyna.db backup: %v", err)
			wait = time.Hour
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// keepCheckpointed moves WAL frames into dyna.db on a timer, so no COMMIT
// pays for the checkpoint while it holds the writer lock. It stops with ctx.
func keepCheckpointed(ctx context.Context, dataDir string) {
	ticker := time.NewTicker(database.CheckpointEvery)
	defer ticker.Stop()
	database.KeepCheckpointed(ctx, dataDir, ticker.C)
}

// recoverStores waits for a known old-format writer to exit before restarting.
// It never migrates while read-only handlers are running. Unknown failures stay
// read-only. Both cases alert through the room feed as soon as routing is ready.
func recoverStores(t *timers, cause error, process func(int) (callerid.Process, error), restart func(), alert func(error)) {
	if cause == nil {
		return // Includes engines explicitly started with --read-only.
	}
	alert(cause)
	var writer *subagent.LivePromptWriterError
	if !errors.As(cause, &writer) {
		return
	}
	var check func()
	check = func() {
		current, err := process(writer.Process.PID)
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != writer.Process) {
			log.Printf("dyna engine: old-format writer exited; restarting to retry migration")
			restart()
			return
		}
		if err != nil {
			log.Printf("dyna engine: check migration writer: %v", err)
		}
		t.at(timerMigrationRetry, t.now().Add(15*time.Second), check)
	}
	t.at(timerMigrationRetry, t.now().Add(15*time.Second), check)
}

func migrationAlert(rooms *room.Store, cause error, wakeRoot func(roomID string)) {
	list, err := rooms.List(false)
	if err != nil {
		log.Printf("dyna engine: read-only alert: %v", err)
		return
	}
	event := room.AlertEvent{
		Source:  room.AlertEngine,
		Summary: "The engine is read-only because migration failed.",
		Remedy:  "systemctl --user restart dyna-engine.service",
		Detail:  cause.Error(),
	}
	for _, rec := range list {
		if _, err := rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: event, Producer: room.ProducerEngine, NeedsRoot: true, To: []string{rec.Name}}); err != nil {
			log.Printf("dyna engine: read-only alert to %s: %v", rec.Name, err)
			continue
		}
		// Read-only mode suppresses the ordinary room wake router. Migration
		// alerts are the one exception, and they select only the room's root.
		wakeRoot(rec.ID)
	}
}
