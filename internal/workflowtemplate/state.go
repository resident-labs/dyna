package workflowtemplate

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
	if err := database.Migrate(db, "workflowtemplate.version", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE workflow_template_seeds (registry TEXT PRIMARY KEY, hashes TEXT NOT NULL)")
		return err
	}); err != nil {
		return err
	}
	return importRegistries(context.Background(), db)
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
	if version != 1 {
		return nil, fmt.Errorf("template seed store is version %d; this dyna needs 1", version)
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

// TODO: legacy/shim remove later — imports previous binary writes through the shared mirror helper; remove with requirement 31.
func importSeedManifest(ctx context.Context, db *sql.DB, registry string) error {
	_, err := state.For(db).ImportIfForeign(ctx, seedMirrorKey(registry), manifestPath(registry), func(conn *sql.Conn, raw []byte) error {
		manifest, err := decodeManagedManifest(raw)
		if err != nil {
			return err
		}
		return saveSeedRecord(ctx, conn, registry, manifest)
	})
	return err
}

// TODO: legacy/shim remove later — supplies the previous release a copy of the current committed row; remove with requirement 31.
func mirrorSeedManifest(ctx context.Context, db *sql.DB, registry string) error {
	return state.For(db).Mirror(ctx, seedMirrorKey(registry), manifestPath(registry), func(out io.Writer) error {
		manifest, err := readSeedRecord(ctx, db, registry)
		if err != nil {
			return err
		}
		body, err := marshalManifest(manifest)
		if err != nil {
			return err
		}
		_, err = out.Write(body)
		return err
	})
}

// TODO: legacy/shim remove later — imports old default and known custom manifests at engine start; remove with requirement 31.
func importRegistries(ctx context.Context, db *sql.DB) error {
	defaultDir, err := filepath.Abs(DefaultRegistryDir())
	if err != nil {
		return err
	}
	registries := map[string]bool{defaultDir: true}
	err = state.For(db).Read(ctx, func(query state.Reader) error {
		rows, err := query.QueryContext(ctx, "SELECT registry FROM workflow_template_seeds")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var registry string
			if err := rows.Scan(&registry); err != nil {
				return err
			}
			registries[registry] = true
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for registry := range registries {
		if err := importRegistry(ctx, db, registry); err != nil {
			if errors.Is(err, ErrBusy) {
				log.Printf("template seed import skipped: %v", err)
				continue
			}
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — serializes startup import with previous binary seed writes; remove with requirement 31.
func importRegistry(ctx context.Context, db *sql.DB, registry string) error {
	unlock, err := tryLockDir(registry)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(registry); os.IsNotExist(err) {
		// Metadata is bounded to registries that still exist. Source is untouched.
		if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, "DELETE FROM workflow_template_seeds WHERE registry=?", registry); err != nil {
				return err
			}
			return state.ForgetMirrors(ctx, conn, seedMirrorKey(registry))
		}); err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}
	return importSeedManifest(ctx, db, registry)
}
