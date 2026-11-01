package ghquota

import (
	"context"
	"database/sql"
	"log"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Migrate creates the engine's bounded endpoint accounting table.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "ghquota.counts", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE ghquota_counts (
			hour INTEGER NOT NULL, kind TEXT NOT NULL, label TEXT NOT NULL,
			counted INTEGER NOT NULL, free INTEGER NOT NULL,
			PRIMARY KEY(hour, kind, label)
		) WITHOUT ROWID`)
		return err
	})
}

// UseStore enables durable counts in the engine. Endpoint labels are not
// worker identities; named sources are stored separately.
func UseStore(w *state.Writer) func() {
	engineGHQuota.mu.Lock()
	defer engineGHQuota.mu.Unlock()
	engineGHQuota.save = func(now time.Time, kind, label, phaseLabel string, free bool) {
		ctx := context.Background()
		hour := now.UTC().Truncate(time.Hour).Unix()
		counted, gratis := 1, 0
		if free {
			counted, gratis = 0, 1
		}
		err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, `INSERT INTO ghquota_counts(hour,kind,label,counted,free)
				VALUES(?,?,?,?,?) ON CONFLICT(hour,kind,label) DO UPDATE SET
				counted=counted+excluded.counted, free=free+excluded.free`,
				hour, kind, label, counted, gratis); err != nil {
				return err
			}
			if phaseLabel != "" {
				if _, err := conn.ExecContext(ctx, `INSERT INTO ghquota_counts(hour,kind,label,counted,free)
					VALUES(?,'endpoint-phase',?,?,?) ON CONFLICT(hour,kind,label) DO UPDATE SET
					counted=counted+excluded.counted, free=free+excluded.free`, hour, phaseLabel, counted, gratis); err != nil {
					return err
				}
			}
			_, err := conn.ExecContext(ctx, "DELETE FROM ghquota_counts WHERE hour < ?", hour-7*24*3600)
			return err
		})
		if err != nil {
			log.Printf("GitHub quota accounting: %v", err)
		}
	}
	q := engineGHQuota
	return func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.save = nil
	}
}
