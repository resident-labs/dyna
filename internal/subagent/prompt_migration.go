package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The cursor commits with each batch, so an interrupted conversion resumes
// after the last converted session_turns rowid.
const promptMigrationKey = "subagent.turns.prompts"
const promptMigrationCompleteKey = promptMigrationKey + ".complete"

const (
	promptBatchRows  = 250
	promptBatchBytes = 1 << 20
)

// TODO: legacy/shim remove later — converts retained inline SQL prompts to
// shared blobs. The existing retained-file importer already uses the blob writer.
func migrateStoredPrompts(db *sql.DB) error {
	ctx := context.Background()
	complete, err := promptMigrationComplete(ctx, db)
	if err != nil || complete {
		return err
	}
	cursor, err := promptMigrationCursor(ctx, db)
	if err != nil {
		return err
	}
	var pending bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_turns WHERE rowid>?)", cursor).Scan(&pending); err != nil {
		return err
	}
	if pending {
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before converting prompts: %w", err)
		}
	}
	for {
		more, err := migratePromptBatch(db, promptBatchRows, promptBatchBytes)
		if err != nil || !more {
			return err
		}
	}
}

// Completion prevents later format 3 turns from restarting the conversion.
func promptMigrationComplete(ctx context.Context, r state.Reader) (bool, error) {
	var complete bool
	err := r.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key=? AND value='1')", promptMigrationCompleteKey).Scan(&complete)
	return complete, err
}

func promptMigrationCursor(ctx context.Context, r state.Reader) (int64, error) {
	var stored string
	err := r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", promptMigrationKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cursor, err := strconv.ParseInt(stored, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s holds %q: %w", promptMigrationKey, stored, err)
	}
	return cursor, nil
}

func migratePromptBatch(db *sql.DB, maxRows, maxBytes int) (bool, error) {
	ctx := context.Background()
	more := false
	err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		complete, err := promptMigrationComplete(ctx, conn)
		if err != nil || complete {
			return err
		}
		cursor, err := promptMigrationCursor(ctx, conn)
		if err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, "SELECT rowid,session,n,turn FROM session_turns WHERE rowid>? ORDER BY rowid LIMIT ?", cursor, maxRows+1)
		if err != nil {
			return err
		}
		type retained struct {
			rowid int64
			id    string
			n     int
			raw   []byte
		}
		var batch []retained
		var size int
		for rows.Next() {
			// Check before adding a row: a batch can exceed the byte bound
			// by at most one row, including when that row alone is larger.
			if len(batch) >= maxRows || size >= maxBytes {
				more = true
				break
			}
			var row retained
			if err := rows.Scan(&row.rowid, &row.id, &row.n, &row.raw); err != nil {
				rows.Close()
				return err
			}
			size += len(row.raw)
			batch = append(batch, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, row := range batch {
			// Resolve only registered references. Hash-shaped inline text
			// remains literal input to the canonical full-text writer.
			resolved, err := resolveRowPrompts(ctx, conn, row.id, row.n, row.raw)
			if err != nil {
				return err
			}
			stored, err := storeTurnPrompts(ctx, conn, row.id, row.n, resolved)
			if err != nil {
				return err
			}
			if !bytes.Equal(stored, row.raw) {
				// This changes storage only, so preserve revision and mirror.
				if _, err := conn.ExecContext(ctx, "UPDATE session_turns SET turn=? WHERE rowid=?", stored, row.rowid); err != nil {
					return err
				}
			}
		}
		if len(batch) > 0 {
			if _, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, promptMigrationKey, strconv.FormatInt(batch[len(batch)-1].rowid, 10)); err != nil {
				return err
			}
		}
		if !more {
			// Commit completion with the final batch, including an empty store.
			_, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES (?,'1') ON CONFLICT(key) DO UPDATE SET value=excluded.value", promptMigrationCompleteKey)
		}
		return err
	})
	return more, err
}
