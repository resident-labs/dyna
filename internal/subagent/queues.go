package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// The session_queues table holds the retired queue blob of earlier releases.
// Nothing reads or writes it any more: a session's claim state lives in its
// delivery rows (see delivery_claims.go and room's deliverystate.go), and
// migrateDeliveryClaims copies a stored blob into those rows once. What is left
// here creates the table and imports a retained queue file into it, so that
// copy has a source on a store that upgraded from a release older than the
// table.
const queueSchema = `CREATE TABLE session_queues (
 session TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 queue TEXT NOT NULL CHECK(json_valid(queue))
);
CREATE UNIQUE INDEX session_queues_revision ON session_queues(revision);`

// queueFile is a retained session queue from before dyna.db.
const queueFile = "queue.json"

func queuesDB(sessions string) (*sql.DB, error) {
	dataDir := filepath.Dir(sessions)
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(db, "subagent.queues", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, queueSchema); err != nil {
				return err
			}
		}
		return importRetainedQueues(ctx, conn, sessions)
	}); err != nil {
		return nil, err
	}
	return db, state.MigrateMirrors(dataDir)
}

// importRetainedQueues runs once inside the queue schema migration. Current
// canonical rows always win; retained files fill only missing rows.
// TODO: legacy/shim remove later — retain this one-time JSON import.
func importRetainedQueues(ctx context.Context, conn *sql.Conn, sessions string) error {
	entries, err := os.ReadDir(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read retained queue folder %s: %w", sessions, err)
	}
	for _, entry := range entries {
		id := entry.Name()
		if !sessionlife.IsSessionID(id) {
			continue
		}
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT 1 FROM session_queues WHERE session=?", id).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained queue folder %s is a symlink", filepath.Join(sessions, id))
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(sessions, id, queueFile)
		_, err = state.ImportMigration(ctx, conn, sessionlife.QueueMirrorKey(id), path, func(conn *sql.Conn, raw []byte) error {
			var queue legacyQueueClaims
			if err := json.Unmarshal(raw, &queue); err != nil {
				return err
			}
			if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
				return errors.New("queue is not a JSON object")
			}
			_, err := saveQueueRow(ctx, conn, id, raw)
			return err
		})
		if err != nil {
			return fmt.Errorf("import retained queue %s: %w", path, err)
		}
	}
	return nil
}

// saveQueueRow writes session id's queue on conn with the next revision. It is
// only the retained-queue import below: nothing reads or writes the queue
// blob after migrateDeliveryClaims moved its claims into the delivery rows.
//
// TODO: legacy/shim remove later — with the session_queues table, once every
// supported store has run subagent.delivery_claims.import and no binary from
// before the delivery rows is alive.
func saveQueueRow(ctx context.Context, conn *sql.Conn, id string, raw []byte) (int64, error) {
	var revision int64
	err := conn.QueryRowContext(ctx, `INSERT INTO session_queues(session, revision, queue)
VALUES (?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_queues), ?)
ON CONFLICT(session) DO UPDATE SET revision = excluded.revision, queue = excluded.queue
RETURNING revision`, id, string(raw)).Scan(&revision)
	return revision, err
}

// migrateQueues prepares the queue table, imports retained queue files, and
// moves their claims into the delivery rows.
func migrateQueues(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	var created int
	if db.QueryRow("SELECT 1 FROM meta WHERE key = 'subagent.queues'").Scan(&created) != nil {
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before queue claims move into it: %w", err)
		}
	}
	if _, err = queuesDB(filepath.Join(dataDir, "sessions")); err != nil {
		return err
	}
	return migrateDeliveryClaims(dataDir)
}
