// Package ancestry stores workflow resume lineage without importing the JavaScript parser.
package ancestry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Store keeps the mutable resume lineage separate from graph blobs.
// A plan has at most one nonempty parent. Its graph identity never changes.
type Store struct{ DataDir string }

func (s Store) db() (*sql.DB, error) {
	if err := state.MigrateMirrors(s.DataDir); err != nil {
		return nil, err
	}
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "plan.ancestry", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE plan_ancestry (plan TEXT PRIMARY KEY, resumed_from TEXT NOT NULL)")
		return err
	})
	return db, err
}

// Migrate imports retained ancestry once at engine start. Imported rows and
// the version commit together; source headers and ownership stamps stay intact.
// Both the default archive and any configured archive root are scanned.
func Migrate(dataDir string) error {
	db, err := (Store{DataDir: dataDir}).db()
	if err != nil {
		return err
	}
	return database.Migrate(db, "plan.ancestry.import", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		roots := []string{filepath.Join(dataDir, "runs"), filepath.Join(dataDir, "archive", "runs")}
		if archive := store.RunArchiveDir(dataDir); archive != roots[1] {
			roots = append(roots, archive)
		}
		for _, root := range roots {
			if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if os.IsNotExist(err) && path == root {
					return nil
				}
				if err != nil {
					return err
				}
				if entry.Name() != "plan.json" {
					return nil
				}
				key, err := planKey(filepath.Dir(path))
				if err != nil {
					return err
				}
				var exists bool
				if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM plan_ancestry WHERE plan=?)", key).Scan(&exists); err != nil {
					return err
				}
				if exists {
					return nil
				}
				imported, err := state.ImportMigration(ctx, conn, "plans/"+key, path, func(conn *sql.Conn, raw []byte) error {
					return importRetainedHeader(ctx, conn, key, raw)
				})
				if err == nil && !imported {
					// Older readers stamped empty headers without storing a row.
					// Preserve that empty value without reading an owned copy.
					_, err = conn.ExecContext(ctx, "INSERT INTO plan_ancestry(plan,resumed_from) VALUES(?, '') ON CONFLICT(plan) DO NOTHING", key)
				}
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: legacy/shim remove later — decodes retained ancestry only at startup.
func importRetainedHeader(ctx context.Context, conn *sql.Conn, key string, raw []byte) error {
	fields, err := decodeHeader(raw)
	if err != nil {
		return err
	}
	var parent string
	if raw, ok := fields["resumedFrom"]; ok {
		if err := json.Unmarshal(raw, &parent); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO plan_ancestry(plan,resumed_from) VALUES(?,?) ON CONFLICT(plan) DO NOTHING", key, strings.TrimSpace(parent))
	return err
}

// planKey preserves a run and its nested workflow identity when it is archived.
// Standalone graph directories use their absolute path.
func planKey(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.Clean(absolute), string(filepath.Separator))
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if strings.HasPrefix(part, "wf_") && len(part) > 3 {
			return filepath.Join(parts[i:]...), nil
		}
	}
	return absolute, nil
}

// Parent reads the authoritative row without inspecting graph files.
func (s Store) Parent(ctx context.Context, dir string) (string, error) {
	key, err := planKey(dir)
	if err != nil {
		return "", err
	}
	db, err := s.db()
	if err != nil {
		return "", err
	}
	return parentRow(ctx, db, key)
}

func parentRow(ctx context.Context, db *sql.DB, key string) (string, error) {
	var parent string
	err := state.For(db).Read(ctx, func(r state.Reader) error {
		err := r.QueryRowContext(ctx, "SELECT resumed_from FROM plan_ancestry WHERE plan=?", key).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return parent, err
}

// LockPath is the ancestry record lock for a run, outside its artifact tree.
func LockPath(dataDir, runID string) string {
	return filepath.Join(dataDir, "run-locks", runID+".plan.lock")
}

func lockID(key string) string {
	if filepath.IsAbs(key) {
		sum := sha256.Sum256([]byte(key))
		return fmt.Sprintf("%x", sum)
	}
	return strings.Split(key, string(filepath.Separator))[0]
}

// beforePlanLock lets tests observe contenders before they acquire the record lock.
var beforePlanLock = func() {}

func (s Store) lock(ctx context.Context, dir string) (func(), error) {
	key, err := planKey(dir)
	if err != nil {
		return nil, err
	}
	path := LockPath(s.DataDir, lockID(key))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	beforePlanLock()
	return store.LockContext(ctx, path)
}

// Stamp records the first nonempty parent without changing the graph.
func (s Store) Stamp(ctx context.Context, dir, parent string) (string, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return s.Parent(ctx, dir)
	}
	key, err := planKey(dir)
	if err != nil {
		return "", err
	}
	db, err := s.db()
	if err != nil {
		return "", err
	}
	unlock, err := s.lock(ctx, dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	var saved string
	// No consumer reads an ancestry event. Publish nothing.
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx, "INSERT INTO plan_ancestry(plan,resumed_from) VALUES(?,?) ON CONFLICT(plan) DO UPDATE SET resumed_from=CASE WHEN plan_ancestry.resumed_from='' THEN excluded.resumed_from ELSE plan_ancestry.resumed_from END RETURNING resumed_from", key, parent).Scan(&saved)
	})
	if err != nil {
		return "", err
	}
	return saved, nil
}

// ForgetAncestry removes a run's lineage and import stamps in its caller's
// transaction. Nested workflow keys share the run's prefix.
func ForgetAncestry(ctx context.Context, conn *sql.Conn, runID string) error {
	if _, err := conn.ExecContext(ctx, "DELETE FROM plan_ancestry WHERE plan=? OR substr(plan,1,length(?))=?", runID, runID+"/", runID+"/"); err != nil {
		return err
	}
	return state.ForgetMirrors(ctx, conn, "plans/"+runID)
}

func decodeHeader(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("plan header must be a JSON object")
	}
	return fields, nil
}
