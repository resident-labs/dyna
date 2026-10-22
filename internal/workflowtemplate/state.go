package workflowtemplate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Store keeps the authoritative seed hashes in dyna.db. Source stays in .js files.
type Store struct{ DataDir string }

// Migrate is called only by the engine, before it serves writes.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "workflowtemplate.version", 2, func(conn *sql.Conn, old int) error {
		if old < 1 {
			if _, err := conn.ExecContext(context.Background(), "CREATE TABLE workflow_template_seeds (registry TEXT PRIMARY KEY, hashes TEXT NOT NULL)"); err != nil {
				return err
			}
		}
		return importRegistries(context.Background(), conn)
	})
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRow("SELECT value FROM meta WHERE key='workflowtemplate.version'").Scan(&version); err != nil {
		return nil, fmt.Errorf("template seed store needs engine migration: %w", err)
	}
	if version != 2 {
		return nil, fmt.Errorf("template seed store is version %d; this dyna needs 2", version)
	}
	return db, nil
}

func readSeedRecord(ctx context.Context, db *sql.DB, registry string) (managedManifest, error) {
	var manifest managedManifest
	err := state.For(db).Read(ctx, func(query state.Reader) error {
		var err error
		manifest, err = seedRecord(ctx, query, registry)
		return err
	})
	return manifest, err
}

func seedRecord(ctx context.Context, q state.Reader, registry string) (managedManifest, error) {
	manifest := managedManifest{Files: map[string]string{}}
	var body string
	err := q.QueryRowContext(ctx, "SELECT hashes FROM workflow_template_seeds WHERE registry=?", registry).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return manifest, nil
	}
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal([]byte(body), &manifest.Files); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func saveSeedRecord(ctx context.Context, conn *sql.Conn, registry string, manifest managedManifest) error {
	body, err := json.Marshal(manifest.Files)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO workflow_template_seeds(registry, hashes) VALUES(?,?) ON CONFLICT(registry) DO UPDATE SET hashes=excluded.hashes", registry, string(body))
	return err
}

// TODO: legacy/shim remove later — keys the shared helper's previous-release copy; remove with requirement 31.
func seedMirrorKey(registry string) string { return "templates/" + registry }

// TODO: legacy/shim remove later — imports old default and known custom manifests at engine start; remove with requirement 31.
func importRegistries(ctx context.Context, conn *sql.Conn) error {
	defaultDir, err := filepath.Abs(DefaultRegistryDir())
	if err != nil {
		return err
	}
	registries := map[string]bool{defaultDir: true}
	rows, err := conn.QueryContext(ctx, "SELECT registry FROM workflow_template_seeds")
	if err != nil {
		return err
	}
	for rows.Next() {
		var registry string
		if err := rows.Scan(&registry); err != nil {
			rows.Close()
			return err
		}
		registries[registry] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for registry := range registries {
		if err := importRegistry(ctx, conn, registry); err != nil {
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — imports an unowned manifest only during migration.
func importRegistry(ctx context.Context, conn *sql.Conn, registry string) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_template_seeds WHERE registry=?)", registry).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	unlock, err := tryLockDir(registry)
	if err != nil {
		return err
	}
	defer unlock()
	return importMissingSeedManifest(ctx, conn, registry)
}

// TODO: legacy/shim remove later — discovers pre-SQLite custom registries on
// first use. The caller holds the registry lock. A canonical row always wins.
func importCustomRegistry(ctx context.Context, db *sql.DB, registry string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_template_seeds WHERE registry=?)", registry).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := os.Lstat(manifestPath(registry)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return importMissingSeedManifest(ctx, conn, registry)
	})
}

// TODO: legacy/shim remove later — imports missing hashes without changing the file or its stamp.
func importMissingSeedManifest(ctx context.Context, conn *sql.Conn, registry string) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workflow_template_seeds WHERE registry=?)", registry).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := state.ImportMigration(ctx, conn, seedMirrorKey(registry), manifestPath(registry), func(conn *sql.Conn, raw []byte) error {
		manifest, err := decodeManagedManifest(raw)
		if err != nil {
			return err
		}
		return saveSeedRecord(ctx, conn, registry, manifest)
	})
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO workflow_template_seeds(registry,hashes) VALUES(?, '{}') ON CONFLICT(registry) DO NOTHING", registry)
	return err
}
