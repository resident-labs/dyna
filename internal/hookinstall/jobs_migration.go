package hookinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
)

const jobsSchema = `CREATE TABLE hook_install_jobs (
 id TEXT PRIMARY KEY,
 archived INTEGER NOT NULL DEFAULT 0,
 body BLOB NOT NULL
)`

func (s Store) dataDir() string { return filepath.Dir(filepath.Dir(s.Root)) }

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.install", 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(context.Background(), jobsSchema); err != nil {
			return err
		}
		return s.importJobs(conn)
	})
	return db, err
}

// Migrate creates the installation receipt table and imports old receipts once.
func Migrate(dataDir string) error {
	_, err := (Store{Root: filepath.Join(dataDir, "hooks", "install")}).db()
	return err
}

// TODO: legacy/shim remove later — imports receipts once and leaves source files intact.
func (s Store) importJobs(conn *sql.Conn) error {
	ctx := context.Background()
	for _, dir := range []struct {
		path     string
		archived int
	}{{s.Root, 0}, {filepath.Join(s.Root, "done"), 1}} {
		paths, err := filepath.Glob(filepath.Join(dir.path, "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			id := strings.TrimSuffix(filepath.Base(path), ".json")
			if !idPattern.MatchString(id) {
				log.Printf("skip install receipt %s: invalid job ID", path)
				continue
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				log.Printf("skip install receipt %s: not a regular file", path)
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var j Job
			if err := json.Unmarshal(raw, &j); err != nil {
				log.Printf("skip install receipt %s: %v", path, err)
				continue
			}
			if j.ID != id {
				log.Printf("skip install receipt %s: job ID does not match filename", path)
				continue
			}
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO hook_install_jobs(id,archived,body) VALUES(?,?,?)", id, dir.archived, raw); err != nil {
				return err
			}
		}
	}
	return nil
}
