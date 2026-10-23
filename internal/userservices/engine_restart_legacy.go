package userservices

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// TODO: legacy/shim remove later — retire the voice restart helper installed by
// older versions before restarting the engine directly.
func retireEngineRestart(ctx context.Context, dir string, run func(...string) (string, error)) error {
	const unit = "dyna-engine-restart.service"
	path := filepath.Join(dir, unit)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := run("stop", unit); err != nil {
		return fmt.Errorf("stop legacy engine restart helper: %w", err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		return err
	}
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM meta WHERE key=?", "userservices.engine-restart:"+filepath.Clean(dir))
		return err
	}); err != nil {
		return err
	}
	return os.Remove(path)
}
