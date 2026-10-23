package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
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
	key := s.key(run.Repo)
	if err := statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return writeRun(ctx, conn, key, run)
	}); err != nil {
		return run, err
	}
	return run, nil
}

func (s Service) runs(ctx context.Context, repo, name string) ([]Run, error) {
	db, err := s.runsDB()
	if err != nil {
		return nil, err
	}
	key := s.key(repo)
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
			if !validRunID(id) || run.ID != id || s.key(run.Repo) != key {
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
