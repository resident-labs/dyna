package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// Queue claims live in dyna.db (rule r10, requirement 20), one row per
// session. readQueue, writeQueue and queueSig are the only code that reads
// or writes them. revision grows with every queue write in the table, so a
// row's revision is the queue's version.
const queueSchema = `CREATE TABLE session_queues (
 session TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 queue TEXT NOT NULL CHECK(json_valid(queue))
);
CREATE UNIQUE INDEX session_queues_revision ON session_queues(revision);`

// queueFile is a session's queue file before dyna.db, and the previous
// release's copy after it.
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
			var queue queueClaims
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

// readQueueRow returns the queue of the session whose folder is dir as JSON,
// and its version. A session with no queue yet returns nil and "". A read
// never writes over the row: without the gate lock it could put an older
// copy back between a writer's commit and its copy, and a claim would come
// undone.
//
// TODO: legacy/shim remove later — the previous release reads and writes
// queue.json. A queue that has only its file is imported on its first read,
// in place of an import walk at engine start; it has no row to write over.
// Current writers keep canonical rows. Remove this missing-row import
// after eager conversion and watcher caller retirement.
func readQueueRow(dir string) ([]byte, string, error) {
	sessions, id := filepath.Dir(dir), filepath.Base(dir)
	db, err := queuesDB(sessions)
	if err != nil {
		return nil, "", err
	}
	raw, version, err := queryQueueRow(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := importQueueFile(db, dir); err != nil {
			return nil, "", err
		}
		raw, version, err = queryQueueRow(db, id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	return raw, version, err
}

func queryQueueRow(db *sql.DB, id string) ([]byte, string, error) {
	var raw []byte
	var revision int64
	if err := db.QueryRow("SELECT queue, revision FROM session_queues WHERE session = ?", id).Scan(&raw, &revision); err != nil {
		return nil, "", err
	}
	return raw, "r" + strconv.FormatInt(revision, 10), nil
}

// importQueueFile fills a missing canonical row from its retained queue file.
// Existing rows always win, including an intentionally empty queue.
func importQueueFile(db *sql.DB, dir string) error {
	id := filepath.Base(dir)
	if _, _, err := queryQueueRow(db, id); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ctx := context.Background()
	_, err := state.For(db).ImportIfForeign(ctx, sessionlife.QueueMirrorKey(id), filepath.Join(dir, queueFile), func(conn *sql.Conn, raw []byte) error {
		if !json.Valid(raw) {
			return fmt.Errorf("%w: not valid JSON; the stored queue stays", state.ErrSkipForeign)
		}
		res, err := conn.ExecContext(ctx, `INSERT INTO session_queues(session, revision, queue)
VALUES (?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_queues), ?)
ON CONFLICT(session) DO NOTHING`, id, string(raw))
		if err != nil {
			return err
		}
		if added, err := res.RowsAffected(); err != nil || added > 0 {
			return err
		}
		return state.ErrRecordExists
	})
	return err
}

// readQueueForUpdate reads the canonical queue while the caller holds the
// gate lock. readQueue imports a retained file only when its row is missing.
func readQueueForUpdate(dir string) (queueClaims, error) {
	return readQueue(dir)
}

// ImportLegacyQueue fills a missing queue row for the engine's watcher.
// It stays until the watcher caller is removed; it never replaces a row.
func (s *Store) ImportLegacyQueue(id string) error {
	return s.withGateLock(id, func(dir string) error {
		db, err := queuesDB(filepath.Dir(dir))
		if err != nil {
			return err
		}
		return importQueueFile(db, dir)
	})
}

// saveQueueRow writes session id's queue on conn with the next revision.
func saveQueueRow(ctx context.Context, conn *sql.Conn, id string, raw []byte) (int64, error) {
	var revision int64
	err := conn.QueryRowContext(ctx, `INSERT INTO session_queues(session, revision, queue)
VALUES (?, (SELECT COALESCE(MAX(revision), 0) + 1 FROM session_queues), ?)
ON CONFLICT(session) DO UPDATE SET revision = excluded.revision, queue = excluded.queue
RETURNING revision`, id, string(raw)).Scan(&revision)
	return revision, err
}

// storeQueue commits session dir's queue, then writes the previous release's
// copy of it.
//
// TODO: legacy/shim remove later — the previous release reads queue.json,
// so a downgrade keeps every claim (requirement 24). Remove the Mirror call
// with item 31.
func storeQueue(dir string, raw []byte) error {
	return storeQueueContext(context.Background(), dir, raw, nil)
}

// storeQueueContext commits the queue and, for a claim call, its exact
// response in the same transaction. A nil queue records only the response.
// A response-only commit publishes no session change.
// The old-release mirror follows the committed queue, as in storeQueue.
func storeQueueContext(ctx context.Context, dir string, raw, reply []byte) error {
	sessions, id := filepath.Dir(dir), filepath.Base(dir)
	db, err := queuesDB(sessions)
	if err != nil {
		return err
	}
	w := state.For(db)
	var event hub.Event
	if raw != nil {
		event = sessionlife.Changed(id)
	}
	if err := w.Write(ctx, event, func(conn *sql.Conn) error {
		if raw != nil {
			if _, err := saveQueueRow(ctx, conn, id, raw); err != nil {
				return err
			}
		}
		if reply != nil {
			return w.FinishRequestIn(ctx, conn, "RunnerClaimInput", reply)
		}
		return nil
	}); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	// The row is the queue: a failed copy only leaves the previous
	// release's view behind, so it is logged, not returned.
	if err := w.Mirror(ctx, sessionlife.QueueMirrorKey(id), filepath.Join(dir, queueFile), func(f io.Writer) error {
		_, err := f.Write(raw)
		return err
	}); err != nil {
		log.Printf("queue of %s: the previous release's copy: %v", id, err)
	}
	return nil
}

// migrateQueues prepares the queue table and imports retained queue files
// before engine readers or watcher recovery can use their claims.
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
	_, err = queuesDB(filepath.Join(dataDir, "sessions"))
	return err
}
