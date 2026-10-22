package computers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// As every store on the rollback helper does, so a process that opens a
	// fresh data folder has the stamp table too.
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "computers.records", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), recordSchema)
		return err
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

// importRecord imports r's file when the previous release wrote it.
//
// TODO: legacy/shim remove later — the previous release reads and writes
// meta.json and state.json in each computer's folder; remove importRecord
// and the copy in saveRecord with requirement 31, when no binary from
// before dyna.db is alive.
func importRecord(ctx context.Context, db *sql.DB, id string, r record) error {
	_, err := state.For(db).ImportIfForeign(ctx, r.key(id), r.path(id), func(conn *sql.Conn, raw []byte) error {
		if !json.Valid(raw) {
			return fmt.Errorf("computer %s %s: %w", id, r.file, state.ErrSkipForeign)
		}
		return setColumn(ctx, conn, id, r, raw)
	})
	return err
}

func setColumn(ctx context.Context, conn *sql.Conn, id string, r record, raw []byte) error {
	// The column name is one of two constants, never input.
	_, err := conn.ExecContext(ctx, `INSERT INTO computers(session, `+r.column+`) VALUES (?, ?)
ON CONFLICT(session) DO UPDATE SET `+r.column+` = excluded.`+r.column, id, raw)
	return err
}

// readRecord reads r for computer id into v, importing the previous
// release's file first; ok is false when there is none. The foreign check
// takes no lock, so a computer whose file is our own copy costs one stat
// and one stamp read; only a foreign file takes the record lock to import.
func readRecord(id string, r record, v any) (ok bool, err error) {
	db, err := recordsDB()
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	foreign, err := state.For(db).Foreign(ctx, r.key(id), r.path(id))
	if err != nil {
		return false, err
	}
	if foreign {
		unlock, err := lockRecord(id)
		if err != nil {
			return false, err
		}
		err = importRecord(ctx, db, id, r)
		unlock()
		if err != nil {
			return false, err
		}
	}
	raw, err := column(ctx, db, id, r)
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

// saveRecord stores v as r for computer id in the order state.Mirror needs:
// import the previous release's file, write the row, then copy it. The
// computer daemon and an offline destroy can both write a computer, so a
// per-computer flock covers the three steps. same, when set, reports
// whether the stored value already matches, so the write is skipped.
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
	if err := importRecord(ctx, db, id, r); err != nil {
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
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return setColumn(ctx, conn, id, r, raw)
	}); err != nil {
		return err
	}
	// TODO: legacy/shim remove later — meta.json and state.json are the
	// previous release's copies; remove with requirement 31.
	return state.For(db).Mirror(ctx, r.key(id), r.path(id), func(out io.Writer) error {
		stored, err := column(ctx, db, id, r)
		if err != nil {
			return err
		}
		_, err = out.Write(stored)
		return err
	})
}

// lockWait bounds the wait for another writer of the same computer, so a
// stuck holder never blocks a supervisor without limit.
const lockWait = 10 * time.Second

// lockRecord takes the per-computer flock that orders this release's
// writers and importers of a computer's records.
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
