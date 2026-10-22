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

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const documentsSchema = `CREATE TABLE hook_documents (
 repo TEXT PRIMARY KEY,
 body BLOB NOT NULL
);`

func (s Service) documentsDB() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.documents", 2, func(conn *sql.Conn, old int) error {
		if old < 1 {
			if _, err := conn.ExecContext(context.Background(), documentsSchema); err != nil {
				return err
			}
		}
		return s.importDocuments(conn)
	})
	return db, err
}

// MigrateDocuments creates the hook configuration table before serving calls.
// Previous-release files import once in the versioned migration.
func MigrateDocuments(dataDir string) error {
	_, err := (Service{DataDir: dataDir}).documentsDB()
	return err
}

func writeDocument(ctx context.Context, conn *sql.Conn, key string, d document) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO hook_documents(repo,body) VALUES(?,?)
ON CONFLICT(repo) DO UPDATE SET body=excluded.body`, key, raw)
	return err
}

type documentQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readDocument(ctx context.Context, q documentQuerier, key string) (document, error) {
	d := document{Hooks: []Hook{}}
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT body FROM hook_documents WHERE repo=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}

// TODO: legacy/shim remove later — names previous-release hook copies in the shared stamp table; remove with item 31.
func documentMirrorKey(key string) string { return "hooks/documents/" + key }

func (s Service) update(ctx context.Context, id string, fn func(*document) error) error {
	db, err := s.documentsDB()
	if err != nil {
		return err
	}
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	unlock, err := store.LockContext(ctx, filepath.Join(dir, "hooks.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, "hooks.json")
	if err := importDocumentLocked(ctx, db, path); err != nil {
		return err
	}
	key := filepath.Base(dir)
	err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		d, err := readDocument(ctx, conn, key)
		if err != nil {
			return err
		}
		if err := fn(&d); err != nil {
			return err
		}
		return writeDocument(ctx, conn, key, d)
	})
	if err != nil {
		return err
	}
	// TODO: legacy/shim remove later — keeps hooks.json as the previous release's write-only rollback copy; remove with item 31.
	err = statewrite.For(db).Mirror(ctx, documentMirrorKey(key), path, func(out io.Writer) error {
		d, err := readDocument(ctx, db, key)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(d)
	})
	if err != nil {
		log.Printf("hook document committed; previous-release copy failed: %v", err)
	}
	return nil
}

// TODO: legacy/shim remove later — imports hook documents from previous-release binaries through the shared stamped-copy helper; remove with item 31.
func (s Service) importDocument(ctx context.Context, db *sql.DB, path string) error {
	key := filepath.Base(filepath.Dir(path))
	foreign, err := statewrite.For(db).Foreign(ctx, documentMirrorKey(key), path)
	if err != nil || !foreign {
		return err
	}
	unlock, err := store.LockContext(ctx, filepath.Join(filepath.Dir(path), "hooks.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return importDocumentLocked(ctx, db, path)
}

// TODO: legacy/shim remove later — imports old hook files while the caller holds hooks.lock; remove with item 31.
func importDocumentLocked(ctx context.Context, db *sql.DB, path string) error {
	key := filepath.Base(filepath.Dir(path))
	_, err := statewrite.For(db).ImportIfForeign(ctx, documentMirrorKey(key), path, func(conn *sql.Conn, raw []byte) error {
		d, err := decodeLegacyDocument(path, raw)
		if err != nil {
			return err
		}
		return writeDocument(ctx, conn, key, d)
	})
	return err
}

// TODO: legacy/shim remove later — shares old-file parsing between engine imports and read-only lifecycle emitters; remove with item 31.
func decodeLegacyDocument(path string, raw []byte) (document, error) {
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("invalid hook configuration in %s: %w", path, err)
	}
	return d, nil
}

// TODO: legacy/shim remove later — walks old hook files for lazy registry discovery; this repeated walk goes with item 31.
func (s Service) legacyDocuments() ([]string, error) {
	return filepath.Glob(filepath.Join(s.dataDir(), "repos", "*", "hooks.json"))
}

func (s Service) load(ctx context.Context, id string) ([]Hook, error) {
	db, err := s.documentsDB()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir(id), "hooks.json")
	foreign := false
	// TODO: legacy/shim remove later — the engine imports old files; other lifecycle emitters read foreign files without importing so blocking hooks stay current. Remove with item 31.
	if database.OwnsSchema() {
		if err := s.importDocument(ctx, db, path); err != nil {
			return nil, err
		}
	} else {
		foreign, err = statewrite.For(db).Foreign(ctx, documentMirrorKey(filepath.Base(s.dir(id))), path)
		if err != nil {
			return nil, err
		}
	}
	var d document
	if foreign {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		d, err = decodeLegacyDocument(path, raw)
	} else {
		d, err = readDocument(ctx, db, filepath.Base(s.dir(id)))
	}
	if err != nil {
		return nil, err
	}
	for i := range d.Hooks {
		d.Hooks[i].Event = NormalizeEvent(d.Hooks[i].Event)
	}
	return d.Hooks, nil
}

// Repositories returns the main checkout paths of configured hooks, including
// repositories whose original hook files have been removed.
func (s Service) Repositories(ctx context.Context) ([]string, error) {
	db, err := s.documentsDB()
	if err != nil {
		return nil, err
	}
	// TODO: legacy/shim remove later — imports hook files still written by old binaries; remove with item 31.
	if database.OwnsSchema() {
		paths, err := s.legacyDocuments()
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if err := s.importDocument(ctx, db, path); err != nil {
				return nil, err
			}
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT body FROM hook_documents ORDER BY repo")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repos := []string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var d document
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		if d.Repo != "" && len(d.Hooks) > 0 {
			repos = append(repos, d.Repo)
		}
	}
	return repos, rows.Err()
}
