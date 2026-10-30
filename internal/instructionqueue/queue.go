// Package instructionqueue stores instruction fan-out until every session is marked.
package instructionqueue

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Topic wakes the instruction worker after a profile intent commits.
const Topic = "instruction-queue"

// Migrate prepares the durable fan-out queue.
func Migrate(dir string) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "instructionqueue.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS instruction_queue (id INTEGER PRIMARY KEY AUTOINCREMENT, room TEXT NOT NULL, changed_at INTEGER NOT NULL)")
		return err
	})
}

// InsertTx records a change in the caller's transaction.
func InsertTx(conn *sql.Conn, room string) error {
	_, err := conn.ExecContext(context.Background(), "INSERT INTO instruction_queue(room,changed_at) VALUES (?,?)", room, time.Now().UnixNano())
	return err
}

// Lock serializes profile writes with replay, so replay cannot consume an
// intent before the corresponding file write.
func Lock(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return store.LockContext(ctx, filepath.Join(dir, "instruction-queue.lock"))
}

// Insert records a profile intent before its file write.
func Insert(dir string) error {
	if err := Migrate(dir); err != nil {
		return err
	}
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{Topic: Topic}, func(conn *sql.Conn) error { return InsertTx(conn, "") })
}

type Entry struct {
	ID        int64
	Room      string
	ChangedAt time.Time
}

// List reads pending changes in commit order.
func List(dir string) ([]Entry, error) {
	if err := Migrate(dir); err != nil {
		return nil, err
	}
	db, err := database.Open(dir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id,room,changed_at FROM instruction_queue ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var at int64
		if err := rows.Scan(&e.ID, &e.Room, &at); err != nil {
			return nil, err
		}
		e.ChangedAt = time.Unix(0, at)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Delete removes only the completed occurrence.
func Delete(dir string, id int64) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), "DELETE FROM instruction_queue WHERE id=?", id)
		return err
	})
}
