package worktrees

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
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

// removeObservedRecord deletes the record only while it still equals the
// version its removal observed. A path recreated and tracked after that keeps
// its new record and its port.
func removeObservedRecord(ctx context.Context, conn *sql.Conn, observed Record) error {
	current, err := loadRecords(ctx, conn, "WHERE path=?", observed.Path)
	if err != nil || len(current) != 1 {
		return err
	}
	want, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	have, err := json.Marshal(current[0])
	if err != nil || !bytes.Equal(have, want) {
		return err
	}
	_, err = conn.ExecContext(ctx, `DELETE FROM worktree_records WHERE path=?`, observed.Path)
	return err
}

// markRemovedBatch deletes the registry records of the removals one cleanup
// pass observed, in one transaction, then rearms the port alert once for the
// whole release. Each record is deleted only while it still equals the
// version its removal observed, so a path recreated meanwhile keeps its new
// record. Tests replace it.
var markRemovedBatch = func(ctx context.Context, removed []Record) error {
	db, err := registryDB()
	if err != nil {
		return err
	}
	releases := make([]portRelease, len(removed))
	for i, rec := range removed {
		releases[i] = portRelease{record: rec, remove: true}
	}
	observation := observePortUseAfterRelease(ctx, db, releases)
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for _, rec := range removed {
			if err := removeObservedRecord(ctx, conn, rec); err != nil {
				return err
			}
		}
		return rearmPortCapacity(ctx, conn, observation)
	})
}

// HandoffRunTx moves registered trees in the run ownership transaction.
func HandoffRunTx(ctx context.Context, conn *sql.Conn, runID, to string) error {
	_, err := conn.ExecContext(ctx, `UPDATE worktree_records SET body=json_set(body,'$.session',?) WHERE run_id=?`, to, runID)
	return err
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
