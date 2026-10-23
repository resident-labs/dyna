package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const runtimeSchema = `CREATE TABLE hook_runtime (
 repo TEXT PRIMARY KEY,
 body BLOB NOT NULL
);`

func (s Service) dataDir() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	return store.DataDir()
}

func (s Service) runtimeDB() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	if err := statewrite.MigrateMirrors(s.dataDir()); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.runtime", 3, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, runtimeSchema); err != nil {
				return err
			}
		} else if old < 2 {
			// Preserve ownership evidence before dropping the old column.
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT 'hooks/runtime/' || repo, mirror FROM hook_runtime WHERE mirror != ''`); err != nil {
				return err
			}
		}
		if err := s.importRuntime(conn); err != nil {
			return err
		}
		if old >= 1 {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE hook_runtime DROP COLUMN mirror"); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, "DELETE FROM meta WHERE key='hooks.runtime.import'")
		return err
	})
	return db, err
}

// TODO: legacy/shim remove later — imports unowned runtime files once at cutover.
func (s Service) importRuntime(conn *sql.Conn) error {
	ctx := context.Background()
	var done int
	err := conn.QueryRowContext(ctx, "SELECT 1 FROM meta WHERE key='hooks.runtime.import'").Scan(&done)
	if err == nil {
		return nil // A completed import owns absence as well as existing rows.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(s.dataDir(), "repos", "*", "state.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		key := filepath.Base(filepath.Dir(path))
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM hook_runtime WHERE repo=?", key).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = statewrite.ImportMigration(ctx, conn, "hooks/runtime/"+key, path, func(conn *sql.Conn, raw []byte) error {
			var st state
			if err := json.Unmarshal(raw, &st); err != nil {
				log.Printf("skip hook runtime %s: %v", path, err)
				return nil
			}
			_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO hook_runtime(repo,body) VALUES(?,?)", key, raw)
			return err
		})
		if err != nil {
			return fmt.Errorf("import hook runtime %s: %w", path, err)
		}
	}
	return nil
}

// Migrate validates pending records and prepares the runtime table.
func Migrate(dataDir string) error {
	s := Service{DataDir: dataDir}
	if err := s.validateStartupSpool(); err != nil {
		return err
	}
	_, err := s.runtimeDB()
	return err
}

func (s Service) runtime(id string, fn func(*state) error) error {
	db, err := s.runtimeDB()
	if err != nil {
		return err
	}
	ctx := context.Background()
	key := filepath.Base(s.dir(id))
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var st state
		var body []byte
		err := conn.QueryRowContext(ctx, "SELECT body FROM hook_runtime WHERE repo=?", key).Scan(&body)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(body) != 0 {
			if err := json.Unmarshal(body, &st); err != nil {
				return err
			}
		}
		if st.Due == nil {
			st.Due = map[string]time.Time{}
		}
		if st.Every == nil {
			st.Every = map[string]int{}
		}
		if st.Notices == nil {
			st.Notices = map[string]bool{}
		}
		if err := fn(&st); err != nil {
			return err
		}
		body, err = json.Marshal(st)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO hook_runtime(repo,body) VALUES(?,?)
ON CONFLICT(repo) DO UPDATE SET body=excluded.body`, key, body)
		return err
	})
}
