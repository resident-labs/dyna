// Package genstate keeps the harness runtime's generation leases and
// publication views in dyna.db. They are queried at cleanup time, so they are
// runtime state rather than configuration (rule r1). Immutable generated
// configs stay in their content-addressed directories.
package genstate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
);
CREATE TABLE IF NOT EXISTS generation_publications (
 harness TEXT NOT NULL CHECK(harness <> ''),
 home TEXT NOT NULL CHECK(home <> ''),
 published_ms INTEGER NOT NULL CHECK(published_ms > 0),
 PRIMARY KEY(harness, home)
);
CREATE TABLE IF NOT EXISTS legacy_generation_views (
 path TEXT PRIMARY KEY NOT NULL CHECK(path <> ''),
 digest TEXT NOT NULL CHECK(digest <> ''),
 modified_ns INTEGER NOT NULL CHECK(modified_ns > 0)
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

// Migrate creates the tables. Only the engine runs it, at start. The records
// that earlier builds kept as JSON files arrive through ImportLegacy, under
// the generation lifecycle lock that the writers of those files take.
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
	// The tables are new in this release: no earlier build wrote them, so the
	// first version creates the final shape.
	err = database.Migrate(db, "harness.generations", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		_, err := conn.ExecContext(ctx, schema)
		return err
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
		return recordViewTx(ctx, conn, view, pending, time.Now())
	})
}

// recordViewTx writes one publication view. A pending publication adds its
// generation, because the home may still read the previous one. A complete
// publication replaces the set and moves the watermark that decides whether a
// legacy view file is older than what this build published.
func recordViewTx(ctx context.Context, conn *sql.Conn, view View, pending bool, now time.Time) error {
	if view.Harness == "" || view.Home == "" || len(view.Generations) == 0 {
		return errors.New("a generation view needs a harness, a home and one generation")
	}
	for _, dir := range view.Generations {
		if dir == "" {
			return errors.New("a generation view names an empty generation")
		}
	}
	if !pending {
		if _, err := conn.ExecContext(ctx, `DELETE FROM generation_views WHERE harness=? AND home=?`, view.Harness, view.Home); err != nil {
			return err
		}
	}
	for _, dir := range view.Generations {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_views(harness,home,generation) VALUES(?,?,?)`,
			view.Harness, view.Home, dir); err != nil {
			return err
		}
	}
	if pending {
		return nil
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO generation_publications(harness,home,published_ms) VALUES(?,?,?)
ON CONFLICT(harness,home) DO UPDATE SET published_ms=excluded.published_ms`, view.Harness, view.Home, now.UnixMilli())
	return err
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
// are read only by ImportLegacy below.
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

// ImportLegacy copies the JSON indexes of earlier builds into the tables. A
// detached runner of an earlier build can still write them, so the runtime
// calls this under the generation lifecycle lock on every cleanup, and a
// written record replaces an earlier copy. An unreadable record fails the
// import: cleanup then deletes nothing, as it did when it read those files.
//
// TODO: legacy/shim remove later — remove with the .uses and .views files of
// earlier builds, once no runner of such a build can be alive.
func (s Store) ImportLegacy(ctx context.Context, generationsRoot string) error {
	db, err := open(s.DataDir)
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if err := importLegacyLeases(ctx, conn, filepath.Join(generationsRoot, ".uses")); err != nil {
			return fmt.Errorf("import generation leases: %w", err)
		}
		return importLegacyViews(ctx, conn, filepath.Join(generationsRoot, ".views"))
	})
}

func importLegacyLeases(ctx context.Context, conn *sql.Conn, dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "use-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var use legacyLease
		if json.Unmarshal(raw, &use) != nil || use.Version != 1 || use.OwnerPID <= 0 || use.Generation == "" {
			return fmt.Errorf("generation use record is invalid: %s", path)
		}
		// The file is authoritative while an earlier build can still rewrite
		// it, so a bound reference or a release replaces the stored row.
		if _, err := conn.ExecContext(ctx, `INSERT INTO generation_leases(id,generation,owner_pid,native,released) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET generation=excluded.generation, owner_pid=excluded.owner_pid, native=excluded.native, released=excluded.released`,
			entry.Name(), use.Generation, use.OwnerPID, string(use.Native), flag(use.Released)); err != nil {
			return err
		}
	}
	return nil
}

func importLegacyViews(ctx context.Context, conn *sql.Conn, dir string) error {
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
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		// Skip only a file this store has already read: the revision is the
		// content and its write time, so an earlier build that rewrites the
		// same JSON records a new publication.
		var storedDigest string
		var storedNs int64
		err = conn.QueryRowContext(ctx, `SELECT digest, modified_ns FROM legacy_generation_views WHERE path=?`, path).Scan(&storedDigest, &storedNs)
		if err == nil && storedDigest == digest && storedNs == info.ModTime().UnixNano() {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var view legacyView
		if json.Unmarshal(raw, &view) != nil || view.Harness == "" || view.Home == "" || len(view.Generations) == 0 {
			return fmt.Errorf("generation view record is invalid: %s", path)
		}
		for _, generation := range view.Generations {
			if generation == "" {
				return fmt.Errorf("generation view record is invalid: %s", path)
			}
		}
		// A complete publication by this build that is at least as new as the
		// file superseded it. A pending publication does not count: its home
		// may still read what the file names.
		var publishedMs int64
		err = conn.QueryRowContext(ctx, `SELECT published_ms FROM generation_publications WHERE harness=? AND home=?`, view.Harness, view.Home).Scan(&publishedMs)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		superseded := publishedMs > 0 && info.ModTime().UnixMilli() <= publishedMs
		if !superseded {
			// Without a later publication of this build, the file may only add
			// to the stored set, which can hold a publication in flight. With
			// one, the file is a newer publication by an earlier build whose
			// set replaces what the home read before.
			if publishedMs > 0 {
				if _, err := conn.ExecContext(ctx, `DELETE FROM generation_views WHERE harness=? AND home=?`, view.Harness, view.Home); err != nil {
					return err
				}
			}
			for _, generation := range view.Generations {
				if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO generation_views(harness,home,generation) VALUES(?,?,?)`, view.Harness, view.Home, generation); err != nil {
					return err
				}
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO legacy_generation_views(path,digest,modified_ns) VALUES(?,?,?)
ON CONFLICT(path) DO UPDATE SET digest=excluded.digest, modified_ns=excluded.modified_ns`, path, digest, info.ModTime().UnixNano()); err != nil {
			return err
		}
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
