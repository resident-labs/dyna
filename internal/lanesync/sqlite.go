package lanesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// One row holds the current snapshot. Updates replace it, so this table
// retains no history and never grows with the number of fetches.
const schema = `CREATE TABLE lane_sync (
 id INTEGER PRIMARY KEY CHECK(id=1),
 snapshot BLOB NOT NULL,
 revision INTEGER NOT NULL,
 legacy_version TEXT NOT NULL DEFAULT ''
);
INSERT INTO lane_sync(id,snapshot,revision) VALUES(1,'{}',0);`

// Migrate prepares lane sync at engine start, then imports sync.json once.
// Later starts only import a file changed by a binary from before SQLite.
func Migrate(dataDir string) error {
	s := &Service{Dir: dataDir}
	db, err := s.open()
	if err != nil {
		return err
	}
	if !database.OwnsSchema() {
		return fmt.Errorf("only the engine imports lane sync state")
	}
	unlock, err := store.Lock(s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	return s.importLegacy(context.Background(), db)
}

func (s *Service) open() (*sql.DB, error) {
	db, err := database.Open(s.Dir)
	if err != nil {
		return nil, err
	}
	// The copy's stamp lives in the shared table, which the version 2 step
	// fills from the old legacy_version column.
	if err := state.MigrateMirrors(s.Dir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "lanesync.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — moves the stamp that #1484 kept in legacy_version into state_mirrors; remove with the column and the copy, when requirement 31 ends.
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT ?, legacy_version FROM lane_sync WHERE id=1 AND legacy_version != ''`, mirrorKey)
		return err
	})
	return db, err
}

// mirrorKey names sync.json in state_mirrors.
const mirrorKey = "lanesync/state"

// TODO: legacy/shim remove later — shares the previous binary's sync.lock path; remove with requirement 31.
func (s *Service) lockPath() string { return filepath.Join(s.Dir, "sync.lock") }

// Read reads only SQLite. The rollback copy is never a read fallback.
func (s *Service) Read() (Snapshot, error) {
	db, err := s.open()
	if err != nil {
		return Snapshot{}, err
	}
	var raw []byte
	if err := db.QueryRow("SELECT snapshot FROM lane_sync WHERE id=1").Scan(&raw); err != nil {
		return Snapshot{}, err
	}
	return decode(raw)
}

func decode(raw []byte) (Snapshot, error) {
	var st Snapshot
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	if st.Repositories == nil {
		st.Repositories = RemoteStatus{}
	}
	if st.Events == nil {
		st.Events = map[string]string{}
	}
	if st.Operations == nil {
		st.Operations = map[string]Operation{}
	}
	if st.Exports == nil {
		st.Exports = map[string]Export{}
	}
	return st, nil
}

func (s *Service) update(ctx context.Context, fn func(*Snapshot) error) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	// TODO: legacy/shim remove later — the previous binary uses this lock.
	// Keep rollback copies in commit order across Service instances and keep
	// old writes out of the copy. Remove with requirement 31.
	unlock, err := store.Lock(s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.importLegacy(ctx, db); err != nil {
		return err
	}
	var st Snapshot
	// Room watch still sends sync changes. Its later bus migration will move
	// that event here; publishing now would send every change twice.
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var raw []byte
		if err := conn.QueryRowContext(ctx, "SELECT snapshot FROM lane_sync WHERE id=1").Scan(&raw); err != nil {
			return err
		}
		var err error
		st, err = decode(raw)
		if err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
		raw, err = json.Marshal(st)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "UPDATE lane_sync SET snapshot=?, revision=revision+1 WHERE id=1", raw)
		return err
	})
	if err != nil {
		return err
	}
	if err := s.mirror(ctx, db); err != nil {
		return fmt.Errorf("lane sync state committed, but rollback copy failed: %w", err)
	}
	return nil
}

// TODO: legacy/shim remove later — sync.json remains a write-only copy for
// the previous release. That release drops dynaRevision when it writes its
// typed Snapshot, which identifies its writes. Remove with requirement 31,
// when no binary from before this release remains.
type legacySnapshot struct {
	Snapshot
	Revision int64 `json:"dynaRevision,omitempty"`
}

// TODO: legacy/shim remove later — the previous release reads sync.json;
// remove this path with its rollback copy when requirement 31 ends.
func (s *Service) path() string { return filepath.Join(s.Dir, "sync.json") }

// TODO: legacy/shim remove later — import old sync.json at engine start or
// before an update, through state.ImportIfForeign. A copy this code wrote
// before its stamps moved to state_mirrors carries dynaRevision, so it is
// stamped without an import. Remove this reader and legacy_version with
// requirement 31.
func (s *Service) importLegacy(ctx context.Context, db *sql.DB) error {
	_, err := state.For(db).ImportIfForeign(ctx, mirrorKey, s.path(), func(conn *sql.Conn, data []byte) error {
		// A damaged file is skipped once; the row stays.
		var old legacySnapshot
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("import lane sync: %v: %w", err, state.ErrSkipForeign)
		}
		if old.Revision > 0 {
			return nil
		}
		raw, err := json.Marshal(old.Snapshot)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "UPDATE lane_sync SET snapshot=?, revision=revision+1 WHERE id=1", raw)
		return err
	})
	return err
}

// TODO: legacy/shim remove later — write the previous release's rollback
// copy after the SQL commit, from the committed row. New code reads only
// SQLite. Remove this copy and its embedded revision with requirement 31.
func (s *Service) mirror(ctx context.Context, db *sql.DB) error {
	return state.For(db).Mirror(ctx, mirrorKey, s.path(), func(out io.Writer) error {
		var raw []byte
		var revision int64
		if err := db.QueryRowContext(ctx, "SELECT snapshot, revision FROM lane_sync WHERE id=1").Scan(&raw, &revision); err != nil {
			return err
		}
		st, err := decode(raw)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(legacySnapshot{Snapshot: st, Revision: revision}, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(append(data, '\n'))
		return err
	})
}
