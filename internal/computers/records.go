package computers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// A computer's registration and its last status live in dyna.db (rule
// r10), one row per session. The computer service writes them from its own
// processes, outside the engine, until item 2 moves those writes into
// engine calls.
const recordSchema = `CREATE TABLE computers (
 session TEXT PRIMARY KEY,
 meta BLOB CHECK(meta IS NULL OR json_valid(meta)),
 status BLOB CHECK(status IS NULL OR json_valid(status))
);`

// Migrate creates the computers table. Only the engine runs it, at start.
func Migrate(dataDir string) error {
	_, err := openRecords(dataDir)
	return err
}

func openRecords(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "computers.records", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, recordSchema); err != nil {
				return err
			}
		}
		return importComputerFiles(ctx, conn, filepath.Join(dataDir, "computers"))
	})
	return db, err
}

func recordsDB() (*sql.DB, error) { return openRecords(filepath.Dir(Root())) }

// A record is one of a computer's two columns and its previous release's
// file in the computer's folder.
type record struct{ column, file string }

var (
	metaRecord   = record{"meta", "meta.json"}
	statusRecord = record{"status", "state.json"}
)

func (r record) path(id string) string { return filepath.Join(Dir(id), r.file) }
func (r record) key(id string) string  { return "computers/" + id + "/" + r.column }

// TODO: legacy/shim remove later — imports retained computer files only during
// schema upgrade; remove once supported stores have passed computers.records 2.
func importComputerFiles(ctx context.Context, conn *sql.Conn, root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || ValidateID(entry.Name()) != nil {
			continue
		}
		id := entry.Name()
		for _, r := range []record{metaRecord, statusRecord} {
			var canonical bool
			if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM computers WHERE session=? AND "+r.column+" IS NOT NULL)", id).Scan(&canonical); err != nil {
				return err
			}
			if canonical {
				continue
			}
			path := filepath.Join(root, id, r.file)
			_, err := state.ImportMigration(ctx, conn, r.key(id), path, func(conn *sql.Conn, raw []byte) error {
				var session string
				if r == metaRecord {
					var value Meta
					if err := json.Unmarshal(raw, &value); err != nil {
						return fmt.Errorf("import computer %s: %w", path, err)
					}
					session = value.SessionID
				} else {
					var value Status
					if err := json.Unmarshal(raw, &value); err != nil {
						return fmt.Errorf("import computer %s: %w", path, err)
					}
					session = value.SessionID
				}
				if session != id {
					return fmt.Errorf("import computer %s: session ID does not match directory", path)
				}
				return setColumn(ctx, conn, id, r, raw)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func setColumn(ctx context.Context, conn *sql.Conn, id string, r record, raw []byte) error {
	// The column name is one of two constants, never input.
	_, err := conn.ExecContext(ctx, `INSERT INTO computers(session, `+r.column+`) VALUES (?, ?)
ON CONFLICT(session) DO UPDATE SET `+r.column+` = excluded.`+r.column, id, raw)
	return err
}

// readRecord reads a canonical SQLite column; ok is false when absent.
func readRecord(id string, r record, v any) (ok bool, err error) {
	db, err := recordsDB()
	if err != nil {
		return false, err
	}
	raw, err := column(context.Background(), db, id, r)
	if err != nil || raw == nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

func column(ctx context.Context, db *sql.DB, id string, r record) ([]byte, error) {
	var raw []byte
	err := state.For(db).Read(ctx, func(q state.Reader) error {
		err := q.QueryRowContext(ctx, `SELECT `+r.column+` FROM computers WHERE session = ?`, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return raw, err
}

// saveRecord stores a canonical column. Its lock orders status comparisons
// across daemon and offline writers. same can skip sample-only changes.
func saveRecord(id string, r record, v any, same func(stored []byte) bool) error {
	unlock, err := lockRecord(id)
	if err != nil {
		return err
	}
	defer unlock()
	ctx := context.Background()
	db, err := recordsDB()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if same != nil {
		stored, err := column(ctx, db, id, r)
		if err != nil {
			return err
		}
		if stored != nil && same(stored) {
			return nil
		}
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return setColumn(ctx, conn, id, r, raw)
	})

}

// lockWait bounds the wait for another writer of the same computer, so a
// stuck holder never blocks a supervisor without limit.
const lockWait = 10 * time.Second

// lockRecord takes the per-computer flock that orders this release's
// writers of a computer's records.
func lockRecord(id string) (func(), error) {
	if err := os.MkdirAll(Dir(id), 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	return store.LockContext(ctx, filepath.Join(Dir(id), "record.lock"))
}

// saveMeta stores a computer's registration.
func saveMeta(m Meta) error { return saveRecord(m.SessionID, metaRecord, m, nil) }

// saveStatus stores a computer's last status, but only when it changed in
// more than its sampled metrics (idle time and memory use), so a run of
// screenshots or samples does not rewrite the row.
func saveStatus(s Status) error {
	return saveRecord(s.SessionID, statusRecord, s, func(stored []byte) bool {
		var prior Status
		if json.Unmarshal(stored, &prior) != nil {
			return false
		}
		a, _ := json.Marshal(withoutSamples(prior))
		b, _ := json.Marshal(withoutSamples(s))
		return string(a) == string(b)
	})
}

// withoutSamples is s without the metrics a computer samples while it runs.
func withoutSamples(s Status) Status {
	s.IdleSeconds, s.RSSKiB, s.MemoryCurrentKiB, s.StreamRSSKiB = 0, 0, 0, 0
	return s
}
