package lanesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// One row holds the current snapshot. Updates replace it, so this table
// retains no history and never grows with the number of fetches.
const schema = `CREATE TABLE lane_sync (
 id INTEGER PRIMARY KEY CHECK(id=1),
 snapshot BLOB NOT NULL,
 revision INTEGER NOT NULL
);`

// Migrate prepares lane sync and imports retained state once.
func Migrate(dataDir string) error {
	if !database.OwnsSchema() {
		return fmt.Errorf("only the engine migrates lane sync state")
	}
	_, err := (&Service{Dir: dataDir}).open()
	return err
}

func (s *Service) open() (*sql.DB, error) {
	db, err := database.Open(s.Dir)
	if err != nil {
		return nil, err
	}
	if err := state.MigrateMirrors(s.Dir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "lanesync.schema", 3, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		} else {
			// Preserve ownership from releases that kept their stamp on the row.
			if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT ?, legacy_version FROM lane_sync WHERE id=1 AND legacy_version != ''`, mirrorKey); err != nil {
				return err
			}
		}
		var count int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM lane_sync WHERE id=1").Scan(&count); err != nil {
			return err
		}
		// An existing row is canonical, even when its snapshot and revision are empty.
		if count == 0 {
			if err := s.importLegacy(ctx, conn); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO lane_sync(id,snapshot,revision) VALUES(1,'{}',0)"); err != nil {
				return err
			}
		}
		if old > 0 {
			_, err := conn.ExecContext(ctx, "ALTER TABLE lane_sync DROP COLUMN legacy_version")
			return err
		}
		return nil
	})
	return db, err
}

// mirrorKey names sync.json in state_mirrors.
const mirrorKey = "lanesync/state"

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
	var st Snapshot
	// Room watch still sends sync changes. Its later bus migration will move
	// that event here; publishing now would send every change twice.
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
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
}

// TODO: legacy/shim remove later — imports sync.json only during schema upgrade;
// remove when all supported data directories have passed lanesync.schema 3.
type legacySnapshot struct {
	Snapshot
	Revision int64 `json:"dynaRevision,omitempty"`
}

func (s *Service) path() string { return filepath.Join(s.Dir, "sync.json") }

func (s *Service) importLegacy(ctx context.Context, conn *sql.Conn) error {
	_, err := state.ImportMigration(ctx, conn, mirrorKey, s.path(), func(conn *sql.Conn, data []byte) error {
		var old legacySnapshot
		// Preserve the former behavior: damaged JSON does not prevent startup.
		if json.Unmarshal(data, &old) != nil || old.Revision > 0 {
			return nil
		}
		raw, err := json.Marshal(old.Snapshot)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO lane_sync(id,snapshot,revision) VALUES(1,?,1)", raw)
		return err
	})
	return err
}
