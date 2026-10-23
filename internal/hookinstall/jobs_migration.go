package hookinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const jobsSchema = `CREATE TABLE hook_install_jobs (
 id TEXT PRIMARY KEY,
 archived INTEGER NOT NULL DEFAULT 0,
 legacy_pending INTEGER NOT NULL DEFAULT 0,
 body BLOB NOT NULL
)`

func (s Store) dataDir() string { return filepath.Dir(filepath.Dir(s.Root)) }

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.dataDir())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "hooks.install", 2, func(conn *sql.Conn, old int) error {
		if old < 1 {
			if _, err := conn.ExecContext(context.Background(), jobsSchema); err != nil {
				return err
			}
			if err := s.importJobs(conn); err != nil {
				return err
			}
		}
		return s.importInstallState(conn)
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
			// Old installers share this lock while they finish their receipt.
			unlock, err := store.Lock(filepath.Join(s.Root, id+".json.lock"))
			if err != nil {
				return err
			}
			err = func() error {
				raw, err := readLegacyJob(path, id)
				if err != nil {
					return err
				}
				if raw == nil {
					return nil
				}
				j, err := decodeJob(raw)
				if err != nil {
					return err
				}
				pending := dir.archived == 0 && j.Status != "finished" && !j.Recorded
				_, err = conn.ExecContext(ctx, "INSERT OR IGNORE INTO hook_install_jobs(id,archived,legacy_pending,body) VALUES(?,?,?,?)", id, dir.archived, pending, raw)
				return err
			}()
			unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// TODO: legacy/shim remove later — reads only receipts selected by the one-time import.
func readLegacyJob(path, id string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		log.Printf("skip install receipt %s: not a regular file", path)
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(raw, &j); err != nil {
		log.Printf("skip install receipt %s: %v", path, err)
		return nil, nil
	}
	if j.ID != id {
		log.Printf("skip install receipt %s: job ID does not match filename", path)
		return nil, nil
	}
	return raw, nil
}

// TODO: legacy/shim remove later — unfinished imported jobs get one terminal handoff.
// The caller holds the old job lock. Only the finite set marked at import can
// read a source; a terminal receipt or canonical completion retires its flag.
func (s Store) finishLegacyJobLocked(db *sql.DB, id string) error {
	var pending bool
	err := db.QueryRow("SELECT legacy_pending FROM hook_install_jobs WHERE id=?", id).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !pending) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	raw, err := readLegacyJob(path, id)
	if err != nil {
		return err
	}
	if raw == nil {
		raw, err = readLegacyJob(filepath.Join(s.Root, "done", id+".json"), id)
		if err != nil {
			return err
		}
	}
	if raw == nil {
		return nil
	}
	j, err := decodeJob(raw)
	if err != nil {
		return err
	}
	if j.Status != "finished" && !j.Recorded {
		return nil
	}
	return statewrite.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE hook_install_jobs SET body=?,legacy_pending=0 WHERE id=? AND legacy_pending=1", raw, id)
		return err
	})
}

// TODO: legacy/shim remove later — checks only unfinished imported job identities.
func (s Store) finishLegacyJob(db *sql.DB, id string) error {
	var pending bool
	err := db.QueryRow("SELECT legacy_pending FROM hook_install_jobs WHERE id=?", id).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !pending) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	unlock, err := store.Lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return s.finishLegacyJobLocked(db, id)
}
