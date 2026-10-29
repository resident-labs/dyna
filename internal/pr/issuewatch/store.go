// Package issuewatch keeps the upstream GitHub issues a room reads, in dyna.db.
// A row is a read cursor, not a copy of the issue: the watcher asks GitHub only
// for comments newer than the cursor, and it sends the saved ETag so an
// unchanged issue costs no REST quota.
package issuewatch

import (
	"context"
	"database/sql"

	"dyna-agent/internal/database"
)

const schemaVersion = 1

const schema = `
CREATE TABLE issue_watches (
	room       TEXT    NOT NULL,
	host       TEXT    NOT NULL,
	repo       TEXT    NOT NULL,
	number     INTEGER NOT NULL,
	title      TEXT    NOT NULL DEFAULT '',
	state      TEXT    NOT NULL DEFAULT '',
	updated    TEXT    NOT NULL DEFAULT '',
	comment_at TEXT    NOT NULL DEFAULT '',
	comment_id INTEGER NOT NULL DEFAULT 0,
	etag       TEXT    NOT NULL DEFAULT '',
	body       TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (room, host, repo, number)
);`

// Watch is the saved read cursor of one issue in one room. Updated is the
// issue's updated_at at the last read. CommentAt is the newest comment
// update seen, and CommentID is the highest comment ID seen.
type Watch struct {
	Room      string
	Host      string
	Repo      string
	Number    int
	Title     string
	State     string
	Updated   string
	CommentAt string
	CommentID int64
	ETag      string
	Body      string
}

// Store keeps issue watches in dataDir's dyna.db.
type Store struct{ DataDir string }

// Migrate brings the issue watch table in dataDir's dyna.db to this version.
// Only the engine calls it, at start.
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "issuewatch.version", schemaVersion, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// Save writes the watch, adding it or replacing its cursor.
func (s Store) Save(ctx context.Context, w Watch) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO issue_watches (room, host, repo, number, title, state, updated, comment_at, comment_id, etag, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (room, host, repo, number) DO UPDATE SET title = excluded.title, state = excluded.state,
		updated = excluded.updated, comment_at = excluded.comment_at, comment_id = excluded.comment_id,
		etag = excluded.etag, body = excluded.body`,
		w.Room, w.Host, w.Repo, w.Number, w.Title, w.State, w.Updated, w.CommentAt, w.CommentID, w.ETag, w.Body)
	return err
}

// Remove deletes one room's watch of an issue. It reports whether a row existed.
func (s Store) Remove(ctx context.Context, room, host, repo string, number int) (bool, error) {
	db, err := s.db()
	if err != nil {
		return false, err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM issue_watches WHERE room = ? AND host = ? AND repo = ? AND number = ?`, room, host, repo, number)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// List returns every watch, in no set order.
func (s Store) List(ctx context.Context) ([]Watch, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT room, host, repo, number, title, state, updated, comment_at, comment_id, etag, body FROM issue_watches`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		var w Watch
		if err := rows.Scan(&w.Room, &w.Host, &w.Repo, &w.Number, &w.Title, &w.State, &w.Updated, &w.CommentAt, &w.CommentID, &w.ETag, &w.Body); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
