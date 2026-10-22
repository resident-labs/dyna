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
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const runtimeSchema = `CREATE TABLE hook_runtime (
 repo TEXT PRIMARY KEY,
 body BLOB NOT NULL,
 mirror TEXT NOT NULL DEFAULT ''
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
	// The runtime copies' stamps live in the shared table, which the version
	// 2 step fills from the old mirror column.
	if err := statewrite.MigrateMirrors(s.dataDir()); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.runtime", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, runtimeSchema); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — moves the stamps that #1480 kept in the mirror column into state_mirrors; remove with the mirror column and the copy, in item 31.
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT 'hooks/runtime/' || repo, mirror FROM hook_runtime WHERE mirror != ''`)
		return err
	})
	return db, err
}

// runtimeKey names a repository's runtime copy in state_mirrors.
func runtimeKey(repo string) string { return "hooks/runtime/" + repo }

// Migrate creates the hook runtime table and imports the old files once.
// Only the engine calls it at start.
func Migrate(dataDir string) error {
	s := Service{DataDir: dataDir}
	if err := s.validateStartupSpool(); err != nil {
		return err
	}
	db, err := s.runtimeDB()
	if err != nil {
		return err
	}
	var done int
	err = db.QueryRow("SELECT 1 FROM meta WHERE key='hooks.runtime.import'").Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(s.dataDir(), "repos", "*", "state.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := s.updateRuntime(filepath.Dir(path), func(*state) error { return nil }); err != nil {
			return fmt.Errorf("import hook runtime %s: %w", path, err)
		}
	}
	return statewrite.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "INSERT OR IGNORE INTO meta(key,value) VALUES('hooks.runtime.import','1')")
		return err
	})
}

func (s Service) runtime(id string, fn func(*state) error) error {
	return s.updateRuntime(s.dir(id), fn)
}

func (s Service) updateRuntime(dir string, fn func(*state) error) error {
	db, err := s.runtimeDB()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// The old lock also orders the post-commit copy and old binary writes.
	unlock, err := store.Lock(filepath.Join(dir, "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, "state.json")
	var st state
	ctx := context.Background()
	key := filepath.Base(dir)
	writer := statewrite.For(db)
	// TODO: legacy/shim remove later — imports a runtime file that an older binary wrote, before this read-modify-write; remove with item 31.
	if _, err := writer.ImportIfForeign(ctx, runtimeKey(key), path, func(conn *sql.Conn, raw []byte) error {
		// A damaged file is skipped once; the row stays.
		if !json.Valid(raw) {
			return fmt.Errorf("hook runtime %s is not JSON: %w", path, statewrite.ErrSkipForeign)
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO hook_runtime(repo,body) VALUES(?,?)
ON CONFLICT(repo) DO UPDATE SET body=excluded.body`, key, raw)
		return err
	}); err != nil {
		return err
	}
	err = writer.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
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
	if err != nil {
		return err
	}
	// TODO: legacy/shim remove later — keeps state.json as a write-only rollback copy for the previous release; remove with item 31.
	if err := writer.Mirror(ctx, runtimeKey(key), path, func(out io.Writer) error {
		return json.NewEncoder(out).Encode(st)
	}); err != nil {
		log.Printf("hook runtime committed; previous-release copy failed: %v", err)
	}
	return nil
}
