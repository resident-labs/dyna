package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// Turn records live in dyna.db (rule r10, requirement 20). ReadTurn,
// writeTurn, Turns and turnVersion are the only code that reads or writes
// them. revision grows with every turn write in the table, so a row's
// revision is its version.
const turnSchema = `CREATE TABLE session_turns (
 session TEXT NOT NULL,
 n INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 turn BLOB NOT NULL,
 mirror TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(session, n)
);
CREATE UNIQUE INDEX session_turns_revision ON session_turns(revision);`

func (s *Store) turnsDB() (*sql.DB, error) {
	dataDir := filepath.Dir(s.root())
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	// The turn copies' stamps live in the shared table, which the version 2
	// step fills from the old mirror column.
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "subagent.turns", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, turnSchema); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — moves the stamps that #1470 kept in the mirror column into state_mirrors; remove with the mirror column and the copy, in item 31.
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO state_mirrors(key, version)
SELECT 'turns/' || session || '/' || n, mirror FROM session_turns WHERE mirror != ''`)
		return err
	})
	if err != nil {
		return nil, err
	}
	// TODO: legacy/shim remove later — earlier runners require subagent.turns version 2; keep the additive report index on its own key until those runners exit.
	err = database.Migrate(db, "subagent.reports", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `CREATE INDEX session_turns_pending_reports ON session_turns(session, n)
WHERE json_extract(CAST(turn AS TEXT), '$.reportPending') = 1`)
		return err
	})
	return db, err
}

// readTurnRow returns turn n of session id as JSON, and its version. A read
// never writes over a row: it may change the row only while a writer holds
// the session lock between its write and its copy, and the row it would
// write back is older.
//
// TODO: legacy/shim remove later — turn records lived in turns/<n>.json
// before dyna.db. A turn that has only its file, from a binary before
// dyna.db, is imported on its first read; it has no row to write over.
// Remove the import here and in readTurnForUpdate and writeTurn with item 31,
// when no runner from before this release is alive.
func (s *Store) readTurnRow(id string, n int) ([]byte, string, error) {
	var raw []byte
	var revision int64
	err := s.queryTurn(id, n, func(db *sql.DB) error {
		var err error
		raw, revision, err = storedTurn(context.Background(), db, id, n)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("session %s has no turn %d", id, n)
	}
	if err != nil {
		return nil, "", err
	}
	return raw, turnRevision(revision), nil
}

func storedTurn(ctx context.Context, q state.Reader, id string, n int) ([]byte, int64, error) {
	var raw []byte
	var revision int64
	err := q.QueryRowContext(ctx, "SELECT turn, revision FROM session_turns WHERE session = ? AND n = ?", id, n).Scan(&raw, &revision)
	return raw, revision, err
}

// queryTurn runs query on the turns database. When turn n has no row, it
// imports the turn's file, if any, and runs query again.
func (s *Store) queryTurn(id string, n int, query func(*sql.DB) error) error {
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	if err := query(db); !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.importTurnFile(id, n, false); err != nil {
		return err
	}
	return query(db)
}

// ImportLegacyTurn imports turn n's file over its row when a runner of an
// earlier release wrote it. It takes the session lock, as every import over
// a row must; the engine's watch calls it for such runners, so their turn
// changes show while they run.
func (s *Store) ImportLegacyTurn(id string, n int) error {
	return s.withLock(id, func() error {
		_, err := s.importTurnFile(id, n, true)
		return err
	})
}

// readTurnForUpdate reads turn n for a read-modify-write. The caller holds
// the session lock, so a file another binary wrote is imported over the row
// first, and the write that follows keeps that binary's change.
func (s *Store) readTurnForUpdate(id string, n int) (Turn, error) {
	if _, err := s.importTurnFile(id, n, true); err != nil {
		return Turn{}, err
	}
	return s.ReadTurn(id, n)
}

func turnRevision(revision int64) string { return "r" + strconv.FormatInt(revision, 10) }

// importTurnFile imports turn n's file when another binary wrote it, and
// returns the turns database. Only a caller that holds the session lock may
// overwrite a row; otherwise the file is imported only where no row exists.
func (s *Store) importTurnFile(id string, n int, overwrite bool) (*sql.DB, error) {
	db, err := s.turnsDB()
	if err != nil {
		return nil, err
	}
	path, err := s.turnPath(id, n)
	if err != nil {
		return nil, err
	}
	_, err = state.For(db).ImportIfForeign(context.Background(), sessionlife.TurnMirrorKey(id, n), path, func(conn *sql.Conn, raw []byte) error {
		// A damaged file is skipped once; the row stays.
		if !json.Valid(raw) {
			return fmt.Errorf("turn %d of %s is not JSON: %w", n, id, state.ErrSkipForeign)
		}
		if !overwrite {
			res, err := conn.ExecContext(context.Background(), `INSERT INTO session_turns(session, n, revision, turn)
VALUES (?, ?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_turns), ?)
ON CONFLICT(session, n) DO NOTHING`, id, n, raw)
			if err != nil {
				return err
			}
			// A writer made the row after the read missed it. Leave the file
			// unstamped, so the next locked write imports it.
			if added, err := res.RowsAffected(); err != nil || added > 0 {
				return err
			}
			return state.ErrRecordExists
		}
		_, err := saveTurnConn(context.Background(), conn, id, n, raw)
		return err
	})
	return db, err
}

// saveTurnRow writes turn n of session id with the next revision.
func saveTurnRow(ctx context.Context, db *sql.DB, id string, n int, raw []byte) (int64, error) {
	var revision int64
	err := state.For(db).Write(ctx, sessionlife.Changed(id), func(conn *sql.Conn) error {
		var err error
		revision, err = saveTurnConn(ctx, conn, id, n, raw)
		return err
	})
	return revision, err
}

func saveTurnConn(ctx context.Context, conn *sql.Conn, id string, n int, raw []byte) (int64, error) {
	var revision int64
	err := conn.QueryRowContext(ctx, `INSERT INTO session_turns(session, n, revision, turn)
VALUES (?, ?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_turns), ?)
ON CONFLICT(session, n) DO UPDATE SET revision = excluded.revision, turn = excluded.turn
RETURNING revision`, id, n, raw).Scan(&revision)
	return revision, err
}

// TODO: legacy/shim remove later — the previous release reads turns/<n>.json,
// so a downgrade keeps every turn (requirement 24). The file is a write-only
// copy of the row, written by state.Mirror after the row's commit. Remove it
// with item 31.
func mirrorTurn(ctx context.Context, db *sql.DB, id string, n int, path string) error {
	return state.For(db).Mirror(ctx, sessionlife.TurnMirrorKey(id, n), path, func(out io.Writer) error {
		var raw []byte
		if err := db.QueryRowContext(ctx, "SELECT turn FROM session_turns WHERE session = ? AND n = ?", id, n).Scan(&raw); err != nil {
			return err
		}
		// The previous release's format: store.WriteJSON's indented JSON.
		var indented bytes.Buffer
		if err := json.Indent(&indented, raw, "", "  "); err != nil {
			return err
		}
		indented.WriteByte('\n')
		_, err := out.Write(indented.Bytes())
		return err
	})
}

// turnVersion is the version of turn n of session id, without decoding it.
func (s *Store) turnVersion(id string, n int) (string, bool) {
	var revision int64
	if err := s.queryTurn(id, n, func(db *sql.DB) error {
		return db.QueryRow("SELECT revision FROM session_turns WHERE session = ? AND n = ?", id, n).Scan(&revision)
	}); err != nil {
		return "", false
	}
	return turnRevision(revision), true
}

// backUpBeforeTurnImports takes the shared backup of dyna.db once per data
// folder, before turn files are first imported (requirement 23). The files
// themselves are imported when each turn is read: every reader names a turn
// by its session and number, from the session's turn count, and reads it
// through importTurnFile.
func backUpBeforeTurnImports(sessions string) error {
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		return err
	}
	var done int
	if db.QueryRow("SELECT 1 FROM meta WHERE key = 'subagent.turns.import'").Scan(&done) == nil {
		return nil
	}
	ids, err := sessionlife.SafeIDs(sessions)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if files, _ := filepath.Glob(filepath.Join(sessions, id, "turns", "*.json")); len(files) > 0 {
			if err := database.BackUpOnce(db); err != nil {
				return fmt.Errorf("back up dyna.db before importing turns: %w", err)
			}
			break
		}
	}
	return database.Migrate(db, "subagent.turns.import", 1, func(*sql.Conn, int) error { return nil })
}
