package engine

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engine/stores"
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
	database.OwnSchema()
	return MigrateStores(dataDir)
}

// startStores migrates dyna.db at engine start. A failed migration does not
// stop the engine: it runs read-only, so it serves no writes, and the
// returned status (Health's status) tells the install why (requirement 32).
func startStores(dataDir string, readOnly bool) (stayReadOnly bool, status string) {
	if err := ownStores(dataDir, readOnly); err != nil {
		database.DisownSchema()
		log.Printf("dyna engine: %v; running read-only", err)
		return true, "read-only: " + err.Error()
	}
	return readOnly, ""
}

// startMode decides how a starting engine runs. A failed migration makes it
// read-only, with the cause as status, but it is still the owner's engine,
// so it keeps Connect: the owner needs remote access to see why. Connect is
// decided on the read-only flag the engine was started with (a second, QA
// engine), not on the read-only state a failure forced.
func startMode(dataDir string, askedReadOnly bool, group, ownerDataDir string) (readOnly bool, status, connectRefused string) {
	readOnly, status = startStores(dataDir, askedReadOnly)
	return readOnly, status, connectRefusal(askedReadOnly, group, dataDir, ownerDataDir)
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
