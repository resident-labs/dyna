package subagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
const turnStoreFormat = 4

const turnSchema = `CREATE TABLE session_turns (
 session TEXT NOT NULL,
 n INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 turn BLOB NOT NULL,
 PRIMARY KEY(session, n)
);
CREATE UNIQUE INDEX session_turns_revision ON session_turns(revision);`

func (s *Store) turnsDB() (*sql.DB, error) {
	dataDir := filepath.Dir(s.root())
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	var version int
	err = db.QueryRow("SELECT value FROM meta WHERE key='subagent.turns'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if version < turnStoreFormat {
		// Direct upgrades must back up before even creating session_turns.
		if err := database.BackUpOnce(db); err != nil {
			return nil, fmt.Errorf("back up dyna.db before retiring turn files: %w", err)
		}
	}
	// Keep the format 3 writer guard before introducing prompt references.
	if version < promptStoreFormat {
		if err := sessionlife.Prepare(s.root()); err != nil {
			return nil, err
		}
		if err := preflightPromptCutover(db, s.root()); err != nil {
			return nil, err
		}
	}
	if err := migratePromptBlobs(db); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "subagent.turns", turnStoreFormat, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, turnSchema); err != nil {
				return err
			}
		}
		if err := s.migrateRetainedTurns(conn); err != nil {
			return err
		}
		if old > 0 {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE session_turns DROP COLUMN mirror"); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS session_turns_pending_reports ON session_turns(session, n)
WHERE json_extract(CAST(turn AS TEXT), '$.reportPending') = 1;
DELETE FROM meta WHERE key='subagent.reports';`)
		return err
	})
	return db, err
}

// readTurnRow reads the canonical turn and its prompt text in one snapshot.
func (s *Store) readTurnRow(id string, n int) ([]byte, string, error) {
	db, err := s.turnsDB()
	if err != nil {
		return nil, "", err
	}
	var raw []byte
	var revision int64
	ctx := context.Background()
	err = state.For(db).Read(ctx, func(r state.Reader) error {
		var err error
		raw, revision, err = resolvedTurn(ctx, r, id, n)
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

// resolvedTurn reads the row and its prompt text through the same snapshot.
func resolvedTurn(ctx context.Context, r state.Reader, id string, n int) ([]byte, int64, error) {
	raw, revision, err := storedTurn(ctx, r, id, n)
	if err == nil {
		raw, err = resolveRowPrompts(ctx, r, id, n, raw)
	}
	return raw, revision, err
}

// readTurnForUpdate reads the canonical turn while the caller holds the session lock.
func (s *Store) readTurnForUpdate(id string, n int) (Turn, error) {
	return s.ReadTurn(id, n)
}

func turnRevision(revision int64) string { return "r" + strconv.FormatInt(revision, 10) }

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

// turnVersion is the version of turn n of session id, without decoding it.
func (s *Store) turnVersion(id string, n int) (string, bool) {
	db, err := s.turnsDB()
	if err != nil {
		return "", false
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM session_turns WHERE session = ? AND n = ?", id, n).Scan(&revision); err != nil {
		return "", false
	}
	return turnRevision(revision), true
}
