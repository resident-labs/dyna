package runstore

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Back up before creating tables or importing retained state.
func backUpRunImport(db *sql.DB) error {
	if !database.OwnsSchema() {
		return nil
	}
	var version string
	err := db.QueryRow("SELECT value FROM meta WHERE key='runs.import'").Scan(&version)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return database.BackUpOnce(db)
}

// TODO: legacy/shim remove later — imports retained meta.json and pause files once.
func migrateMeta(db *sql.DB, dataDir string) error {
	return database.Migrate(db, "runs.import", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		roots := []string{filepath.Join(dataDir, "runs"), filepath.Join(dataDir, "archive", "runs")}
		if archive := store.RunArchiveDir(dataDir); archive != roots[1] {
			roots = append(roots, archive)
		}
		for _, root := range roots {
			entries, err := os.ReadDir(root)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				id := entry.Name()
				if !entry.IsDir() || validateRunID(id) != nil {
					continue
				}
				// A row or any mirror stamp proves canonical ownership. Do not inspect
				// stale copies, including copies left after an intentional deletion.
				var owned bool
				if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE id=?) OR EXISTS(SELECT 1 FROM state_mirrors WHERE key=?)", id, metaKey(id)).Scan(&owned); err != nil {
					return err
				}
				if owned {
					continue
				}
				dir := filepath.Join(root, id)
				_, err = state.ImportMigration(ctx, conn, metaKey(id), filepath.Join(dir, "meta.json"), func(conn *sql.Conn, raw []byte) error {
					if err := importMeta(id)(conn, raw); err != nil {
						return err
					}
					info, err := os.Lstat(filepath.Join(dir, "paused"))
					if os.IsNotExist(err) {
						return nil
					}
					if err != nil {
						return err
					}
					if info.Mode().IsRegular() {
						_, err = conn.ExecContext(ctx, `UPDATE runs SET body=json_set(body,'$.paused',json('true')) WHERE id=? AND status='running'`, id)
					}
					return err
				})
				if errors.Is(err, state.ErrSkipForeign) {
					log.Printf("run %s: %v", id, err)
					continue
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}
