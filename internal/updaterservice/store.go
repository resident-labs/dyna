// Package updaterservice stores the source updater's configuration.
package updaterservice

import (
	"context"
	"database/sql"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// RunName identifies installer receipts created by the Updates service.
const RunName = "updates-service"

// Config is the source updater's machine-wide configuration.
type Config struct {
	Source        string
	Enabled       bool
	EveryMinutes  int
	MultipleHooks bool
}

// Store holds the machine-wide updater configuration in the shared database.
type Store struct{ DataDir string }

func (s Store) Get(ctx context.Context) (Config, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return Config{}, err
	}
	var c Config
	err = db.QueryRowContext(ctx, `SELECT source,enabled,every_minutes,multiple_hooks FROM updater_service WHERE id=1`).Scan(&c.Source, &c.Enabled, &c.EveryMinutes, &c.MultipleHooks)
	return c, err
}

// ClaimInterval atomically claims a due source update without replaying missed intervals.
func (s Store) ClaimInterval(ctx context.Context, now time.Time) (Config, bool, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	var due string
	claimed := false
	err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if err := conn.QueryRowContext(ctx, `SELECT source,enabled,every_minutes,multiple_hooks,next_due FROM updater_service WHERE id=1`).Scan(&c.Source, &c.Enabled, &c.EveryMinutes, &c.MultipleHooks, &due); err != nil {
			return err
		}
		if !c.Enabled || c.Source == "" || c.MultipleHooks {
			return nil
		}
		if due != "" {
			t, err := time.Parse(time.RFC3339Nano, due)
			if err != nil {
				return err
			}
			if now.Before(t) {
				return nil
			}
		}
		claimed = true
		_, err := conn.ExecContext(ctx, `UPDATE updater_service SET next_due=? WHERE id=1`, now.Add(time.Duration(c.EveryMinutes)*time.Minute).UTC().Format(time.RFC3339Nano))
		return err
	})
	return c, claimed, err
}

// Set replaces the updater configuration in one committed write.
func (s Store) Set(ctx context.Context, c Config) error {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE updater_service SET source=?, enabled=?, every_minutes=?, multiple_hooks=? WHERE id=1`, c.Source, c.Enabled, c.EveryMinutes, c.MultipleHooks)
		return err
	})
}
