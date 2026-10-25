// Package modelcooldown keeps the failure time of each model of each profile
// in dyna.db. A profile's turns can run in different processes, so the state a
// provider rejection leaves must be shared: a process-local map let a new turn
// runner retry a model that another process had just seen fail.
package modelcooldown

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// schemaVersion is this store's version in dyna.db's meta table.
const schemaVersion = 1

// One row per model of a profile holds its newest failure time.
const schema = `
CREATE TABLE harness_model_cooldowns (
	profile   TEXT    NOT NULL,
	model     TEXT    NOT NULL,
	failed_at INTEGER NOT NULL,
	PRIMARY KEY (profile, model)
);`

// Store reads and writes cooldown rows in one data folder. The zero value uses
// the owner's data folder, so every turn runner process shares its rows.
type Store struct{ DataDir string }

// Migrate creates the cooldown table. Only the engine runs it, at start,
// because a detached turn runner must not change the schema.
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

// dataDir resolves the folder this store uses. A caller that names one gets
// independent rows, which a test uses for a reader in its own process.
func (s Store) dataDir() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	return store.DataDir()
}

// db opens dyna.db and brings this store's table to the current version. The
// engine migrates it; any other process finds the table the engine left.
func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "harness.modelcooldowns", schemaVersion, func(conn *sql.Conn, old int) error {
		if old < 1 {
			_, err := conn.ExecContext(context.Background(), schema)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// Record keeps the newest failure time of one model of one profile. An older
// observation never shortens a cooldown that a newer failure started.
func (s Store) Record(ctx context.Context, profileName, model string, at time.Time) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO harness_model_cooldowns(profile, model, failed_at)
VALUES (?, ?, ?)
ON CONFLICT(profile, model) DO UPDATE SET failed_at = MAX(harness_model_cooldowns.failed_at, excluded.failed_at)`,
			profileName, model, at.UnixMilli())
		return err
	})
}

// Clear deletes one model's failure, because a turn on that model succeeded.
func (s Store) Clear(ctx context.Context, profileName, model string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `DELETE FROM harness_model_cooldowns WHERE profile = ? AND model = ?`, profileName, model)
		return err
	})
}

// Failures returns the failure time of each recorded model of one profile. A
// caller compares each time with the cooldown window it applies.
func (s Store) Failures(ctx context.Context, profileName string) (map[string]time.Time, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	failures := map[string]time.Time{}
	err = state.For(db).Read(ctx, func(q state.Reader) error {
		rows, err := q.QueryContext(ctx, `SELECT model, failed_at FROM harness_model_cooldowns WHERE profile = ?`, profileName)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var model string
			var millis int64
			if err := rows.Scan(&model, &millis); err != nil {
				return err
			}
			failures[model] = time.UnixMilli(millis)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return failures, nil
}
