package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
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
	err = database.Migrate(db, "hooks.documents", 4, func(conn *sql.Conn, old int) error {
		if old < 1 {
			if _, err := conn.ExecContext(context.Background(), documentsSchema); err != nil {
				return err
			}
		}
		if old < 2 {
			if err := s.importDocuments(conn); err != nil {
				return err
			}
		}
		if err := migrateDocumentFields(conn); err != nil {
			return err
		}
		return migrateDocumentScope(conn)
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
	for i := range d.Hooks {
		// A hook stored before scope existed belongs to its repository.
		if d.Hooks[i].Scope == "" {
			d.Hooks[i].Scope = ScopeRepository
		}
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
	key := s.key(id)
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		d, err := readDocument(ctx, conn, key)
		if err != nil {
			return err
		}
		if err := fn(&d); err != nil {
			return err
		}
		return writeDocument(ctx, conn, key, d)
	})
}

func (s Service) load(ctx context.Context, id string) ([]Hook, error) {
	db, err := s.documentsDB()
	if err != nil {
		return nil, err
	}
	d, err := readDocument(ctx, db, s.key(id))
	if err != nil {
		return nil, err
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

// documentRepo returns the main checkout that one hook document records. The
// document remains after its last hook is removed, and a settled run needs the
// checkout to reach the room its card is in.
func (s Service) documentRepo(ctx context.Context, id string) (string, error) {
	db, err := s.documentsDB()
	if err != nil {
		return "", err
	}
	d, err := readDocument(ctx, db, s.key(id))
	if err != nil {
		return "", err
	}
	return d.Repo, nil
}
