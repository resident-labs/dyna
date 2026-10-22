package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const schema = `CREATE TABLE notices (
 id TEXT PRIMARY KEY,
 position INTEGER NOT NULL,
 key TEXT NOT NULL,
 kind TEXT NOT NULL,
 severity TEXT NOT NULL,
 text TEXT NOT NULL,
 command TEXT NOT NULL,
 at TEXT NOT NULL
);
CREATE TABLE notices_revision (n INTEGER NOT NULL);
INSERT INTO notices_revision(n) VALUES (0);`

const mirrorKey = "notices/list"

// Migrate creates tables only. Legacy notices are imported on first read.
func Migrate(dataDir string) error {
	_, err := (Store{Dir: dataDir}).open()
	return err
}

func (s Store) open() (*sql.DB, error) {
	if err := state.MigrateMirrors(s.Dir); err != nil {
		return nil, err
	}
	db, err := database.Open(s.Dir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "notices.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	return db, err
}

func readNotices(q state.Reader) ([]Notice, error) {
	rows, err := q.QueryContext(context.Background(), "SELECT id,key,kind,severity,text,command,at FROM notices ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Notice{}
	for rows.Next() {
		var n Notice
		var at string
		if err := rows.Scan(&n.ID, &n.Key, &n.Kind, &n.Severity, &n.Text, &n.Command, &at); err != nil {
			return nil, err
		}
		if n.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

func (s Store) snapshot(db *sql.DB) (list []Notice, err error) {
	err = state.For(db).Read(context.Background(), func(q state.Reader) error {
		list, err = readNotices(q)
		return err
	})
	return list, err
}

// Revision never decreases, including when the newest notice is dismissed.
func (s Store) Revision() (int64, error) {
	db, err := s.open()
	if err != nil {
		return 0, err
	}
	if err := s.importForeign(db); err != nil {
		return 0, err
	}
	var rev int64
	err = state.For(db).Read(context.Background(), func(q state.Reader) error {
		return q.QueryRowContext(context.Background(), "SELECT n FROM notices_revision").Scan(&rev)
	})
	return rev, err
}

// saveNotices changes only rows whose contents or order changed. The revision
// survives row deletion, so dismissing the newest notice never reuses a version.
func saveNotices(conn *sql.Conn, list []Notice) error {
	ctx := context.Background()
	old, err := readNotices(conn)
	if err != nil {
		return err
	}
	kept := make(map[string]bool, len(list))
	for _, n := range list {
		kept[n.ID] = true
	}
	before := make(map[string]int, len(old))
	for i, n := range old {
		before[n.ID] = i
		if !kept[n.ID] {
			if _, err := conn.ExecContext(ctx, "DELETE FROM notices WHERE id=?", n.ID); err != nil {
				return err
			}
		}
	}
	for i, n := range list {
		if j, ok := before[n.ID]; ok && i == j && old[j] == n {
			continue
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO notices(id,position,key,kind,severity,text,command,at) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET position=excluded.position,key=excluded.key,kind=excluded.kind,severity=excluded.severity,text=excluded.text,command=excluded.command,at=excluded.at`, n.ID, i, n.Key, n.Kind, n.Severity, n.Text, n.Command, n.At.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "UPDATE notices_revision SET n=n+1")
	return err
}

func (s Store) write(db *sql.DB, fn func(*[]Notice)) error {
	// TODO: legacy/shim remove later — the room watcher still owns notices events;
	// publish the row once when item 16 moves this topic onto the bus.
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		list, err := readNotices(conn)
		if err != nil {
			return err
		}
		fn(&list)
		return saveNotices(conn, list)
	})
}

// TODO: legacy/shim remove later — import notices.json when an old binary
// replaces it; remove with the rollback mirror after item 31.
func (s Store) importForeign(db *sql.DB) error {
	foreign, err := state.For(db).Foreign(context.Background(), mirrorKey, s.Path())
	if err != nil || !foreign {
		return err
	}
	unlock, err := s.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return s.importLocked(db)
}

// TODO: legacy/shim remove later — old binaries write notices.json under
// notices.lock; keep import, write, and mirror under that lock until item 31.
func (s Store) importLocked(db *sql.DB) error {
	_, err := state.For(db).ImportIfForeign(context.Background(), mirrorKey, s.Path(), func(conn *sql.Conn, raw []byte) error {
		var list []Notice
		if err := json.Unmarshal(raw, &list); err != nil {
			return fmt.Errorf("%w: notices: %v", state.ErrSkipForeign, err)
		}
		ids, keys := map[string]bool{}, map[string]bool{}
		for _, n := range list {
			if ids[n.ID] || keys[n.Key] {
				return fmt.Errorf("%w: duplicate notice ID or key", state.ErrSkipForeign)
			}
			ids[n.ID], keys[n.Key] = true, true
		}
		return saveNotices(conn, list)
	})
	return err
}

// TODO: legacy/shim remove later — keep notices.json readable by the previous
// release for rollback; remove with foreign-file import after item 31.
func (s Store) mirror(db *sql.DB) error {
	return state.For(db).Mirror(context.Background(), mirrorKey, s.Path(), func(out io.Writer) error {
		list, err := s.snapshot(db)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	})
}

// Callers that receive a request context can cancel before entering the writer queue.
// TODO: legacy/shim remove later — retain the previous release's notices.lock until all writers use the engine.
func (s Store) lock(ctx context.Context) (func(), error) {
	return store.LockContext(ctx, filepath.Join(s.Dir, "notices.lock"))
}
