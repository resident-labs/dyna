package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// migrateStores lets startup tests inject a failing migration. Production and
// the fake engine both run the shared ordered registry.
var migrateStores = stores.Migrate

// ownStores makes this engine the one process that migrates dyna.db, and
// migrates every store. A read-only engine never writes, so it does neither
// and fails on an old store like any other process.
func ownStores(dataDir string, readOnly bool) error {
	if readOnly {
		return nil
	}
	if err := profile.Migrate(profile.DefaultPath()); err != nil {
		return err
	}
	database.OwnSchema()
	return MigrateStores(dataDir)
}

// startStores migrates dyna.db at engine start. A failed migration does not
// stop the engine: it runs read-only, so it serves no writes, and the
// returned status (Health's status) tells the install why (requirement 32).
func startStores(dataDir string, readOnly bool) (stayReadOnly bool, status string, cause error) {
	if err := ownStores(dataDir, readOnly); err != nil {
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
	readOnly, status, cause = startStores(dataDir, askedReadOnly)
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

// recoverStores waits for a known old-format writer to exit before restarting.
// It never migrates while read-only handlers are running. Unknown failures stay
// read-only. Both cases alert through the room feed once after two minutes.
func recoverStores(t *timers, cause error, process func(int) (callerid.Process, error), restart func(), alert func(error)) {
	if cause == nil {
		return // Includes engines explicitly started with --read-only.
	}
	t.at(timerMigrationAlert, t.now().Add(2*time.Minute), func() { alert(cause) })
	var writer *subagent.LivePromptWriterError
	if !errors.As(cause, &writer) {
		return
	}
	var check func()
	check = func() {
		current, err := process(writer.Process.PID)
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != writer.Process) {
			t.stop(timerMigrationAlert)
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

func migrationAlert(rooms *room.Store, cause error) {
	list, err := rooms.List(false)
	if err != nil {
		log.Printf("dyna engine: read-only alert: %v", err)
		return
	}
	for _, rec := range list {
		_, err := rooms.Post(rec.ID, room.PostInput{
			Wake: true, NeedsRoot: true, From: "dyna-engine", Kind: "alert", To: []string{rec.Name},
			Text: fmt.Sprintf("The engine has been read-only for more than 2 minutes because migration failed: %v", cause),
		})
		if err != nil {
			log.Printf("dyna engine: read-only alert to %s: %v", rec.Name, err)
		}
	}
}
