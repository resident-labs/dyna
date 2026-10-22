// Package ancestry stores workflow resume lineage without importing the JavaScript parser.
package ancestry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Migrate creates the ancestry table at engine start.
// Legacy headers are imported on read; startup never inspects graph files.
func Migrate(dataDir string) error {
	_, err := (Store{DataDir: dataDir}).db()
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

// Parent imports a foreign legacy header, then reads the authoritative row.
func (s Store) Parent(ctx context.Context, dir string) (string, error) {
	key, err := planKey(dir)
	if err != nil {
		return "", err
	}
	db, err := s.db()
	if err != nil {
		return "", err
	}
	if err := s.importForeign(ctx, db, dir); err != nil {
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

// beforePlanLock lets tests hold contenders after their foreign-header checks,
// before either can acquire the record lock.
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

// TODO: legacy/shim remove later — import an older binary's plan header on its first read; remove with item 31 after all older workflow runners exit.
func (s Store) importForeign(ctx context.Context, db *sql.DB, dir string) error {
	key, err := planKey(dir)
	if err != nil {
		return err
	}
	foreign, err := state.For(db).Foreign(ctx, "plans/"+key, filepath.Join(dir, "plan.json"))
	if err != nil || !foreign {
		return err
	}
	unlock, err := s.lock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	return s.importHeader(ctx, db, dir)
}

// Stamp records the first nonempty parent, then updates the downgrade header.
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
	if err := s.importHeader(ctx, db, dir); err != nil {
		return "", err
	}
	var saved string
	// No consumer reads an ancestry event. Publish nothing.
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx, "INSERT INTO plan_ancestry(plan,resumed_from) VALUES(?,?) ON CONFLICT(plan) DO UPDATE SET resumed_from=CASE WHEN plan_ancestry.resumed_from='' THEN excluded.resumed_from ELSE plan_ancestry.resumed_from END RETURNING resumed_from", key, parent).Scan(&saved)
	})
	if err != nil {
		return "", err
	}
	if err := s.mirror(ctx, db, key, dir); err != nil {
		return saved, fmt.Errorf("plan ancestry committed; rollback header: %w", err)
	}
	return saved, nil
}

// TODO: legacy/shim remove later — import a previous binary's first parent before reads or writes; remove with item 31 after all older workflow runners exit.
func (s Store) importHeader(ctx context.Context, db *sql.DB, dir string) error {
	key, err := planKey(dir)
	if err != nil {
		return err
	}
	_, err = state.For(db).ImportIfForeign(ctx, "plans/"+key, filepath.Join(dir, "plan.json"), func(conn *sql.Conn, raw []byte) error {
		fields, err := decodeHeader(raw)
		if err != nil {
			// A damaged graph is an unreadable plan. Skipping it would falsely
			// report no parent and let archive selection remove a needed run.
			return err
		}
		var parent string
		if raw, ok := fields["resumedFrom"]; ok {
			if err := json.Unmarshal(raw, &parent); err != nil {
				return err
			}
		}
		parent = strings.TrimSpace(parent)
		if parent == "" {
			return nil // ImportIfForeign still stamps this file version.
		}
		// A nonempty row wins over a stale header, including a crash before
		// its rollback copy was written. Old binaries can only fill an empty row.
		_, err = conn.ExecContext(ctx, "INSERT INTO plan_ancestry(plan,resumed_from) VALUES(?,?) ON CONFLICT(plan) DO UPDATE SET resumed_from=excluded.resumed_from WHERE plan_ancestry.resumed_from=''", key, parent)
		return err
	})
	return err
}

// TODO: legacy/shim remove later — plan.json is a write-only rollback copy of resume ancestry for item 24; remove with item 31 after all older workflow runners exit.
func (s Store) mirror(ctx context.Context, db *sql.DB, key, dir string) error {
	return state.For(db).Mirror(ctx, "plans/"+key, filepath.Join(dir, "plan.json"), func(w io.Writer) error {
		raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
		if err != nil {
			return err
		}
		fields, err := decodeHeader(raw)
		if err != nil {
			return err
		}
		parent, err := parentRow(ctx, db, key)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(parent)
		if err != nil {
			return err
		}
		fields["resumedFrom"] = encoded
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(fields)
	})
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
