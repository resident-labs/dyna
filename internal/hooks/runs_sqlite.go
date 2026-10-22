package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const runsSchema = `CREATE TABLE hook_runs (
 repo TEXT NOT NULL,
 id TEXT NOT NULL,
 name TEXT NOT NULL,
 body BLOB NOT NULL,
 PRIMARY KEY(repo, id)
);`

func (s Service) runsDB() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.runs", 2, func(conn *sql.Conn, old int) error {
		if old < 1 {
			if _, err := conn.ExecContext(context.Background(), runsSchema); err != nil {
				return err
			}
		}
		return s.importRuns(conn)
	})
	return db, err
}

// MigrateRuns creates the hook run table before the engine serves requests.
// Previous-release receipts import once in the versioned migration.
func MigrateRuns(dataDir string) error {
	_, err := (Service{DataDir: dataDir}).runsDB()
	return err
}

func validRunID(id string) bool { return len(id) == 32 && filepath.Base(id) == id }

func writeRun(ctx context.Context, conn *sql.Conn, key string, run Run) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO hook_runs(repo,id,name,body) VALUES(?,?,?,?)
ON CONFLICT(repo,id) DO UPDATE SET name=excluded.name, body=excluded.body`, key, run.ID, run.Name, raw)
	return err
}

// TODO: legacy/shim remove later — names previous-release run copies in the shared stamp table; remove with item 31.
func runMirrorKey(key, id string) string { return "hooks/runs/" + key + "/" + id }

func (s Service) saveRun(ctx context.Context, run Run) (Run, error) {
	if !validRunID(run.ID) {
		return run, fmt.Errorf("invalid hook run ID")
	}
	db, err := s.runsDB()
	if err != nil {
		return run, err
	}
	dir := filepath.Join(s.dir(run.Repo), "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return run, err
	}
	key := filepath.Base(s.dir(run.Repo))
	path := filepath.Join(dir, run.ID+".json")
	unlock, err := store.LockContext(ctx, path+".lock")
	if err != nil {
		return run, err
	}
	defer unlock()
	// Keep the record lock across import, commit and copy, including readers
	// that can import a file written by a previous-release process.
	if err := s.importRunLocked(ctx, db, key, path); err != nil {
		return run, err
	}
	if err := statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return writeRun(ctx, conn, key, run)
	}); err != nil {
		return run, err
	}
	// TODO: legacy/shim remove later — keeps each run JSON as a write-only previous-release rollback copy; remove with item 31.
	err = statewrite.For(db).Mirror(ctx, runMirrorKey(key, run.ID), path, func(out io.Writer) error {
		var raw []byte
		if err := db.QueryRowContext(ctx, "SELECT body FROM hook_runs WHERE repo=? AND id=?", key, run.ID).Scan(&raw); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(json.RawMessage(raw))
	})
	if err != nil {
		log.Printf("hook run committed; previous-release copy failed: %v", err)
	}
	return run, nil
}

// TODO: legacy/shim remove later — imports run files from previous-release binaries through the shared stamped-copy helper; remove with item 31.
func (s Service) importRun(ctx context.Context, db *sql.DB, key, path string) error {
	id := strings.TrimSuffix(filepath.Base(path), ".json")
	foreign, err := statewrite.For(db).Foreign(ctx, runMirrorKey(key, id), path)
	if err != nil || !foreign {
		return err
	}
	unlock, err := store.LockContext(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return s.importRunLocked(ctx, db, key, path)
}

// TODO: legacy/shim remove later — imports old run files while the caller holds path+".lock"; remove with item 31.
func (s Service) importRunLocked(ctx context.Context, db *sql.DB, key, path string) error {
	id := strings.TrimSuffix(filepath.Base(path), ".json")
	_, err := statewrite.For(db).ImportIfForeign(ctx, runMirrorKey(key, id), path, func(conn *sql.Conn, raw []byte) error {
		if !validRunID(id) {
			return fmt.Errorf("%w: invalid hook run ID %q", statewrite.ErrSkipForeign, id)
		}
		var run Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return fmt.Errorf("%w: hook run JSON: %v", statewrite.ErrSkipForeign, err)
		}
		if run.ID != id || filepath.Base(s.dir(run.Repo)) != key {
			return fmt.Errorf("%w: hook run identity does not match %s", statewrite.ErrSkipForeign, path)
		}
		return writeRun(ctx, conn, key, run)
	})
	return err
}

func (s Service) runs(ctx context.Context, repo, name string) ([]Run, error) {
	db, err := s.runsDB()
	if err != nil {
		return nil, err
	}
	key := filepath.Base(s.dir(repo))
	// TODO: legacy/shim remove later — finds run files written by older binaries for lazy import; remove with item 31.
	paths, err := filepath.Glob(filepath.Join(s.dir(repo), "runs", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if err := s.importRun(ctx, db, key, path); err != nil {
			return nil, err
		}
	}
	query := "SELECT body FROM hook_runs WHERE repo=?"
	args := []any{key}
	if name != "" {
		query += " AND name=?"
		args = append(args, "hook-"+name)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartedAt.Equal(result[j].StartedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartedAt.Before(result[j].StartedAt)
	})
	return result, nil
}

// TODO: legacy/shim remove later — imports retained receipts once during startup.
func (s Service) importRuns(conn *sql.Conn) error {
	ctx := context.Background()
	paths, err := filepath.Glob(filepath.Join(s.dataDir(), "repos", "*", "runs", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		key := filepath.Base(filepath.Dir(filepath.Dir(path)))
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM hook_runs WHERE repo=? AND id=?", key, id).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = statewrite.ImportMigration(ctx, conn, runMirrorKey(key, id), path, func(conn *sql.Conn, raw []byte) error {
			var run Run
			if err := json.Unmarshal(raw, &run); err != nil {
				log.Printf("skip hook receipt %s: %v", path, err)
				return nil
			}
			if !validRunID(id) || run.ID != id || filepath.Base(s.dir(run.Repo)) != key {
				log.Printf("skip hook receipt %s: invalid run or repository identity", path)
				return nil
			}
			_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO hook_runs(repo,id,name,body) VALUES(?,?,?,?)", key, id, run.Name, raw)
			return err
		})
		if err != nil {
			return fmt.Errorf("import hook receipt %s: %w", path, err)
		}
	}
	return nil
}
