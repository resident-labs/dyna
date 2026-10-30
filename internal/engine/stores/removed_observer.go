package stores

import (
	"context"
	"database/sql"
	"dyna-agent/internal/database"
)

func migrateRemovedObserver(dir string) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "removed_turn_observer.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `DROP TABLE IF EXISTS admission_observed; DROP TABLE IF EXISTS admission_state; DELETE FROM meta WHERE key IN ('admission_observed.version', 'admission_state.version')`)
		return err
	})
}
