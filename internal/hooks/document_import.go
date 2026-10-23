package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	statewrite "dyna-agent/internal/state"
)

// TODO: legacy/shim remove later — imports retained hook documents once during startup.
func (s Service) importDocuments(conn *sql.Conn) error {
	ctx := context.Background()
	paths, err := filepath.Glob(filepath.Join(s.dataDir(), "repos", "*", "hooks.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		key := filepath.Base(filepath.Dir(path))
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM hook_documents WHERE repo=?", key).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = statewrite.ImportMigration(ctx, conn, documentMirrorKey(key), path, func(conn *sql.Conn, raw []byte) error {
			if _, err := decodeLegacyDocument(path, raw); err != nil {
				return err
			}
			// Keep the full source, including fields this release does not understand.
			_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO hook_documents(repo,body) VALUES(?,?)", key, raw)
			return err
		})
		if err != nil {
			return fmt.Errorf("import hook document %s: %w", path, err)
		}
	}
	return nil
}

// TODO: legacy/shim remove later — decodes retained hook sources only during migration.
func decodeLegacyDocument(path string, raw []byte) (document, error) {
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("invalid hook configuration in %s: %w", path, err)
	}
	return d, nil
}
