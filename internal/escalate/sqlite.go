package escalate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Migrate imports retained dedupe records at engine start without changing them.
func Migrate(dataDir string) error {
	_, err := openRecords(dataDir)
	return err
}

func openRecords(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "escalate.version", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, `CREATE TABLE escalation_records (
 key TEXT PRIMARY KEY,
 session TEXT NOT NULL,
 profile TEXT NOT NULL,
 at TEXT NOT NULL
)`); err != nil {
				return err
			}
		}
		// Canonical rows own their derived source paths, including malformed or
		// nonregular stale copies. Check ownership before touching retained files.
		rows, err := conn.QueryContext(ctx, "SELECT key FROM escalation_records")
		if err != nil {
			return err
		}
		owned := map[string]bool{}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			owned[dedupeHash(key)[:16]+".json"] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		paths, err := filepath.Glob(filepath.Join(dataDir, "escalations", "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			if owned[filepath.Base(path)] {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("escalation record %s is not a regular file", path)
			}
			// Required sources must name their own derived path before import.
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var rec record
			if err := json.Unmarshal(raw, &rec); err != nil {
				return fmt.Errorf("read escalation %s: %w", path, err)
			}
			if rec.Key == "" || rec.Session == "" || rec.At.IsZero() || filepath.Base(path) != dedupeHash(rec.Key)[:16]+".json" {
				return fmt.Errorf("invalid escalation record %s", path)
			}
			if _, err := state.ImportMigration(ctx, conn, "escalations/"+rec.Key, path, func(conn *sql.Conn, raw []byte) error {
				var imported record
				if err := json.Unmarshal(raw, &imported); err != nil {
					return err
				}
				if imported.Key != rec.Key || imported.Session != rec.Session || imported.Profile != rec.Profile || !imported.At.Equal(rec.At) {
					return fmt.Errorf("escalation record %s changed during migration", path)
				}
				return putRecord(ctx, conn, imported)
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return db, err
}

func readRecord(ctx context.Context, w *state.Writer, key string) (record, bool, error) {
	var rec record
	var at string
	found := false
	err := w.Read(ctx, func(q state.Reader) error {
		err := q.QueryRowContext(ctx, "SELECT key, session, profile, at FROM escalation_records WHERE key = ?", key).Scan(&rec.Key, &rec.Session, &rec.Profile, &at)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rec.At, err = time.Parse(time.RFC3339Nano, at)
		found = err == nil
		return err
	})
	return rec, found, err
}

func putRecord(ctx context.Context, conn *sql.Conn, rec record) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO escalation_records (key, session, profile, at) VALUES (?, ?, ?, ?)
 ON CONFLICT(key) DO NOTHING`, rec.Key, rec.Session, rec.Profile, rec.At.Format(time.RFC3339Nano))
	return err
}

func saveRecord(ctx context.Context, w *state.Writer, rec record) error {
	return w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return putRecord(ctx, conn, rec) })
}
