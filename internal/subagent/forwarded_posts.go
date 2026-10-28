package subagent

import (
	"context"
	"database/sql"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// forwardedDB holds one receipt per source session and original post.
func (s *Store) forwardedDB() (*sql.DB, error) {
	if err := sessionlife.Prepare(s.root()); err != nil {
		return nil, err
	}
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "subagent.forwarded_posts", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, `CREATE TABLE session_forwarded_posts (
   session_id TEXT NOT NULL, post_id TEXT NOT NULL, forward_id TEXT NOT NULL,
   PRIMARY KEY(session_id, post_id)
  )`); err != nil {
			return err
		}
		// TODO: legacy/shim remove later — import the short-lived JSON receipt field
		// written by the first D-405 build after all old builds are retired.
		_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO session_forwarded_posts(session_id,post_id,forward_id)
SELECT s.id,j.key,j.value FROM session_records s, json_each(s.meta,'$.forwardedPosts') j
WHERE json_type(s.meta,'$.forwardedPosts')='object' AND j.type='text'`)
		return err
	})
	return db, err
}

func (s *Store) forwardedPosts(id string) (map[string]string, error) {
	db, err := s.forwardedDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT post_id, forward_id FROM session_forwarded_posts WHERE session_id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var post, forward string
		if err := rows.Scan(&post, &forward); err != nil {
			return nil, err
		}
		out[post] = forward
	}
	return out, rows.Err()
}

func (s *Store) recordForwarded(ctx context.Context, session, post, forward string) error {
	db, err := s.forwardedDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, sessionlife.Changed(session), func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO session_forwarded_posts(session_id,post_id,forward_id) VALUES(?,?,?)", session, post, forward)
		return err
	})
}
