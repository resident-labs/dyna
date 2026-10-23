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
	// Check live writers before changing the reader boundary. Once format 3
	// is stored, ordinary reads never inspect processes again.
	var version int
	err = db.QueryRow("SELECT value FROM meta WHERE key='subagent.turns'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if version < promptStoreFormat {
		if err := sessionlife.Prepare(s.root()); err != nil {
			return nil, err
		}
		if err := preflightPromptCutover(db, s.root()); err != nil {
			return nil, err
		}
	}
	// Version 3 stores prompt references. Unconverted inline rows remain
	// readable until the bounded startup conversion reaches them.
	err = database.Migrate(db, "subagent.turns", promptStoreFormat, func(conn *sql.Conn, old int) error {
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
	if err := migratePromptBlobs(db); err != nil {
		return nil, err
	}
	// Keep the existing report index migration on its original key.
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
// dyna.db, is imported on its first read; existing canonical rows always win.
// Remove this missing-row import after eager conversion and watcher retirement.
func (s *Store) readTurnRow(id string, n int) ([]byte, string, error) {
	var raw []byte
	var revision int64
	err := s.queryTurn(id, n, func(db *sql.DB) error {
		ctx := context.Background()
		return state.For(db).Read(ctx, func(r state.Reader) error {
			var err error
			raw, revision, err = resolvedTurn(ctx, r, id, n)
			return err
		})
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

// resolvedTurn reads the row and its prompt text through the same snapshot.
func resolvedTurn(ctx context.Context, r state.Reader, id string, n int) ([]byte, int64, error) {
	raw, revision, err := storedTurn(ctx, r, id, n)
	if err == nil {
		raw, err = resolveRowPrompts(ctx, r, id, n, raw)
	}
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
	if _, err := s.importTurnFile(id, n); err != nil {
		return err
	}
	return query(db)
}

// ImportLegacyTurn fills a missing canonical row for the engine's watcher.
// It stays until the watcher caller is removed; it never replaces a row.
func (s *Store) ImportLegacyTurn(id string, n int) error {
	return s.withLock(id, func() error {
		_, err := s.importTurnFile(id, n)
		return err
	})
}

// readTurnForUpdate reads the canonical turn while the caller holds the
// session lock. ReadTurn imports a retained file only when its row is missing.
func (s *Store) readTurnForUpdate(id string, n int) (Turn, error) {
	return s.ReadTurn(id, n)
}

func turnRevision(revision int64) string { return "r" + strconv.FormatInt(revision, 10) }

// importTurnFile fills a missing canonical row from its retained turn file.
// Existing rows always win, including intentionally cleared fields.
func (s *Store) importTurnFile(id string, n int) (*sql.DB, error) {
	db, err := s.turnsDB()
	if err != nil {
		return nil, err
	}
	var exists int
	err = db.QueryRow("SELECT 1 FROM session_turns WHERE session=? AND n=?", id, n).Scan(&exists)
	if err == nil {
		return db, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	path, err := s.turnPath(id, n)
	if err != nil {
		return nil, err
	}
	_, err = state.For(db).ImportIfForeign(context.Background(), sessionlife.TurnMirrorKey(id, n), path, func(conn *sql.Conn, raw []byte) error {
		// Recheck inside the transaction in case a writer made the row
		// after the read missed it. Leave its retained file unstamped.
		var exists int
		err := conn.QueryRowContext(context.Background(), "SELECT 1 FROM session_turns WHERE session=? AND n=?", id, n).Scan(&exists)
		if err == nil {
			return state.ErrRecordExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("turn %d of %s is not JSON: %w", n, id, state.ErrSkipForeign)
		}
		_, err = saveTurnConn(context.Background(), conn, id, n, raw)
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

// saveTurnConn accepts full text, never an encoded stored row. Rewrites
// must resolve the stored row before passing it here.
func saveTurnConn(ctx context.Context, conn *sql.Conn, id string, n int, raw []byte) (int64, error) {
	raw, err := storeTurnPrompts(ctx, conn, id, n, raw)
	if err != nil {
		return 0, err
	}
	var revision int64
	err = conn.QueryRowContext(ctx, `INSERT INTO session_turns(session, n, revision, turn)
VALUES (?, ?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_turns), ?)
ON CONFLICT(session, n) DO UPDATE SET revision = excluded.revision, turn = excluded.turn
RETURNING revision`, id, n, raw).Scan(&revision)
	return revision, err
}

// TODO: legacy/shim remove later — the previous release reads turns/<n>.json,
// so a downgrade keeps every turn (requirement 24). The file is a write-only
// copy of the row, written by state.MirrorCanonical after the row's commit.
// Remove with P1828a.
func mirrorTurn(ctx context.Context, db *sql.DB, id string, n int, path string) error {
	return state.For(db).MirrorCanonical(ctx, sessionlife.TurnMirrorKey(id, n), path, func(out io.Writer) error {
		var raw []byte
		if err := state.For(db).Read(ctx, func(r state.Reader) error {
			var err error
			raw, _, err = resolvedTurn(ctx, r, id, n)
			return err
		}); err != nil {
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
