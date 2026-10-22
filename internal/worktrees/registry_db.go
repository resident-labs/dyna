package worktrees

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

const registrySchema = `CREATE TABLE IF NOT EXISTS worktree_records (
 path TEXT PRIMARY KEY, run_id TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body))
);
CREATE INDEX IF NOT EXISTS worktree_records_run ON worktree_records(run_id);`

// Migrate prepares the registry and imports old JSON records at engine start.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open worktree registry: %w", err)
	}
	return migrateRegistry(db, dataDir)
}

func registryDB() (*sql.DB, error) {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return nil, fmt.Errorf("open worktree registry: %w", err)
	}
	if err := migrateRegistry(db, store.DataDir()); err != nil {
		return nil, err
	}
	return db, nil
}

func migrateRegistry(db *sql.DB, dataDir string) error {
	err := database.Migrate(db, "worktrees.schema", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, registrySchema); err != nil {
			return err
		}
		var marker string
		err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='worktrees.imported'`).Scan(&marker)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if err := importLegacyRecords(ctx, conn, filepath.Join(dataDir, "worktrees")); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('worktrees.imported','1')`)
		return err
	})
	if err != nil {
		return fmt.Errorf("prepare worktree registry: %w", err)
	}
	return nil
}

// TODO: legacy/shim remove later — imports the JSON worktree records written before dyna.db; remove with the JSON files in a later cleanup PR, after a rollback to the JSON build is no longer needed.
func importLegacyRecords(ctx context.Context, conn *sql.Conn, dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read worktree registry: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// A read failure rolls back the import, so the next call retries it.
			return fmt.Errorf("read legacy worktree record %s: %w", entry.Name(), err)
		}
		rec, err := decodeLegacyRecord(data)
		if err != nil {
			// A damaged or unsafe file is skipped and never imported.
			continue
		}
		if err := saveRecord(ctx, conn, rec); err != nil {
			return err
		}
	}
	// Valid JSON files stay on disk, so a rollback to the JSON build still finds them.
	return nil
}

// TODO: legacy/shim remove later — decodes one JSON worktree record for the one-time import; remove with importLegacyRecords.
func decodeLegacyRecord(data []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, err
	}
	safe, err := validateRecordPath(rec)
	if err != nil {
		return Record{}, err
	}
	rec.Path = safe
	return rec, nil
}

// saveRecord inserts or replaces the record for its path.
func saveRecord(ctx context.Context, conn *sql.Conn, rec Record) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode worktree record: %w", err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO worktree_records(path,run_id,body) VALUES(?,?,?)
ON CONFLICT(path) DO UPDATE SET run_id=excluded.run_id, body=excluded.body`, rec.Path, rec.RunID, string(body))
	if err != nil {
		return fmt.Errorf("write worktree registry record: %w", err)
	}
	return nil
}

type reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// loadRecords returns the records that match where, in path order. An empty where matches all.
func loadRecords(ctx context.Context, q reader, where string, args ...any) ([]Record, error) {
	rows, err := q.QueryContext(ctx, "SELECT body FROM worktree_records "+where+" ORDER BY path", args...)
	if err != nil {
		return nil, fmt.Errorf("read worktree registry: %w", err)
	}
	defer rows.Close()
	records := []Record{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("read worktree registry: %w", err)
		}
		var rec Record
		if err := json.Unmarshal([]byte(body), &rec); err != nil {
			return nil, fmt.Errorf("decode worktree record: %w", err)
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
