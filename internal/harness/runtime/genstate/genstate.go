// Package genstate keeps the harness runtime's generation leases and
// publication views in dyna.db. They are queried at cleanup time, so they are
// runtime state rather than configuration (rule r1). Immutable generated
// configs stay in their content-addressed directories.
package genstate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

const schema = `CREATE TABLE IF NOT EXISTS generation_leases (
 id TEXT PRIMARY KEY NOT NULL CHECK(id <> ''),
 generation TEXT NOT NULL CHECK(generation <> ''),
 owner_pid INTEGER NOT NULL CHECK(owner_pid > 0),
 native TEXT NOT NULL DEFAULT '',
 released INTEGER NOT NULL DEFAULT 0 CHECK(released IN (0,1))
);
CREATE TABLE IF NOT EXISTS generation_views (
 harness TEXT NOT NULL CHECK(harness <> ''),
 home TEXT NOT NULL CHECK(home <> ''),
 generation TEXT NOT NULL CHECK(generation <> ''),
 PRIMARY KEY(harness, home, generation)
);`

// Lease is one process's claim on a generation while its native conversation
// can still run. Native is the process reference the liveness probe reads.
type Lease struct {
	ID         string
	Generation string
	OwnerPID   int
	Native     []byte
	Released   bool
}

// View is the account-home publication of one harness: the generations a home
// still reads while a new publication is pending. Gen is one generation the
// home keeps.
type View struct {
	Harness, Home string
	Generations   []string
}

// Store reads and writes the runtime state in the data folder's dyna.db.
type Store struct{ DataDir string }

// Migrate creates the tables and imports the records that earlier builds kept
// as JSON files. Only the engine runs it, at start.
func Migrate(dataDir string) error {
	_, err := open(dataDir)
	return err
}

func open(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, errors.New("generation state needs a data directory")
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "harness.generations", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		return importLegacyState(ctx, conn, filepath.Join(dataDir, "harnesses", "generations"))
	})
	return db, err
}

func (s Store) db() (*sql.DB, error) { return open(s.DataDir) }

// Create records one lease. A repeated ID keeps its row.
func (s Store) Create(ctx context.Context, lease Lease) error {
	if lease.ID == "" || lease.Generation == "" || lease.OwnerPID <= 0 {
		return errors.New("a generation lease needs an ID, a generation and its process")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_leases(id,generation,owner_pid,native,released) VALUES(?,?,?,?,?)`,
			lease.ID, lease.Generation, lease.OwnerPID, string(lease.Native), flag(lease.Released))
		return err
	})
}

// Bind replaces the lease's process reference.
func (s Store) Bind(ctx context.Context, id string, native []byte) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE generation_leases SET native=? WHERE id=?`, string(native), id)
		if err != nil {
			return err
		}
		return changed(result, id)
	})
}

// Release marks a lease released: its generation stays until the process that
// could still use it is gone.
func (s Store) Release(ctx context.Context, id string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE generation_leases SET released=1 WHERE id=?`, id)
		if err != nil {
			return err
		}
		return changed(result, id)
	})
}

// Delete removes one lease.
func (s Store) Delete(ctx context.Context, id string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `DELETE FROM generation_leases WHERE id=?`, id)
		return err
	})
}

// Leases lists every lease, oldest first.
func (s Store) Leases(ctx context.Context) ([]Lease, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var out []Lease
	err = state.For(db).Read(ctx, func(reader state.Reader) error {
		rows, err := reader.QueryContext(ctx, `SELECT id,generation,owner_pid,native,released FROM generation_leases ORDER BY rowid`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var lease Lease
			var native string
			var released int
			if err := rows.Scan(&lease.ID, &lease.Generation, &lease.OwnerPID, &native, &released); err != nil {
				return err
			}
			lease.Native, lease.Released = []byte(native), released != 0
			out = append(out, lease)
		}
		return rows.Err()
	})
	return out, err
}

// RecordView stores the generations a published account home keeps. A pending
// publication adds to the set, because the home may still read the previous
// generation; a complete one replaces it.
func (s Store) RecordView(ctx context.Context, view View, pending bool) error {
	if view.Harness == "" || view.Home == "" || len(view.Generations) == 0 {
		return errors.New("a generation view needs a harness, a home and one generation")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if !pending {
			if _, err := conn.ExecContext(ctx, `DELETE FROM generation_views WHERE harness=? AND home=?`, view.Harness, view.Home); err != nil {
				return err
			}
		}
		for _, dir := range view.Generations {
			if dir == "" {
				return errors.New("a generation view names an empty generation")
			}
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_views(harness,home,generation) VALUES(?,?,?)`, view.Harness, view.Home, dir); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteView removes one harness and home's publication view, for a harness
// this release no longer runs.
func (s Store) DeleteView(ctx context.Context, harness, home string) error {
	if harness == "" || home == "" {
		return errors.New("a generation view needs a harness and a home")
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `DELETE FROM generation_views WHERE harness=? AND home=?`, harness, home)
		return err
	})
}

// Views lists the publication views, one row per harness and home, ordered by
// harness and generation.
func (s Store) Views(ctx context.Context) ([]View, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var out []View
	index := map[string]int{}
	err = state.For(db).Read(ctx, func(reader state.Reader) error {
		rows, err := reader.QueryContext(ctx, `SELECT harness,home,generation FROM generation_views ORDER BY harness,home,generation`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var harness, home, dir string
			if err := rows.Scan(&harness, &home, &dir); err != nil {
				return err
			}
			key := harness + "\x00" + home
			i, ok := index[key]
			if !ok {
				i = len(out)
				index[key] = i
				out = append(out, View{Harness: harness, Home: home})
			}
			out[i].Generations = append(out[i].Generations, dir)
		}
		return rows.Err()
	})
	return out, err
}

func flag(on bool) int {
	if on {
		return 1
	}
	return 0
}

func changed(result sql.Result, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("generation lease %s does not exist", id)
	}
	return nil
}

// legacyLease and legacyView are the JSON records earlier builds wrote. They
// are read only by the one-time import below.
type legacyLease struct {
	Version    int             `json:"version"`
	Generation string          `json:"generation"`
	OwnerPID   int             `json:"ownerPid"`
	Released   bool            `json:"released,omitempty"`
	Native     json.RawMessage `json:"native,omitempty"`
}

type legacyView struct {
	Harness, Home string
	Generations   []string
}

// TODO: legacy/shim remove later — import the .uses and .views JSON indexes of
// earlier builds once, when the engine migrates. Remove with the files.
func importLegacyState(ctx context.Context, conn *sql.Conn, root string) error {
	importLeases := func() error {
		dir := filepath.Join(root, ".uses")
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || len(entry.Name()) <= len("use-.json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var use legacyLease
			if json.Unmarshal(raw, &use) != nil || use.Version != 1 || use.OwnerPID <= 0 || use.Generation == "" {
				log.Printf("generation state: skipped unreadable lease %s", path)
				continue
			}
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_leases(id,generation,owner_pid,native,released) VALUES(?,?,?,?,?)`,
				entry.Name(), use.Generation, use.OwnerPID, string(use.Native), flag(use.Released)); err != nil {
				return err
			}
		}
		return nil
	}
	importViews := func() error {
		dir := filepath.Join(root, ".views")
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var view legacyView
			if json.Unmarshal(raw, &view) != nil || view.Harness == "" || view.Home == "" || len(view.Generations) == 0 {
				log.Printf("generation state: skipped unreadable view %s", path)
				continue
			}
			for _, dir := range view.Generations {
				if dir == "" {
					continue
				}
				if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_views(harness,home,generation) VALUES(?,?,?)`, view.Harness, view.Home, dir); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := importLeases(); err != nil {
		return fmt.Errorf("import generation leases: %w", err)
	}
	if err := importViews(); err != nil {
		return fmt.Errorf("import generation views: %w", err)
	}
	return nil
}

// NewID names a lease. The prefix keeps a database row recognizable beside the
// files earlier builds wrote.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("use-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return "use-" + hex.EncodeToString(b[:])
}
