package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
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

// Migrate imports retained notices before the engine serves calls.
func Migrate(dataDir string) error {
	_, err := (Store{Dir: dataDir}).open()
	return err
}

func (s Store) open() (*sql.DB, error) {
	db, err := database.Open(s.Dir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "notices.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old == 0 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		// Rows and committed empty revisions take precedence over retained files.
		var owned bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM notices) OR EXISTS(SELECT 1 FROM notices_revision WHERE n>0)").Scan(&owned); err != nil {
			return err
		}
		if owned {
			return nil
		}
		// TODO: legacy/shim remove later — read notices.json once during startup migration.
		_, err := state.ImportMigration(ctx, conn, mirrorKey, s.Path(), func(conn *sql.Conn, raw []byte) error {
			var list []Notice
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("invalid notices: %w", err)
			}
			ids, keys := map[string]bool{}, map[string]bool{}
			for _, n := range list {
				if ids[n.ID] || keys[n.Key] {
					return fmt.Errorf("duplicate notice ID or key")
				}
				ids[n.ID], keys[n.Key] = true, true
			}
			if len(list) == 0 {
				return nil
			}
			return saveNotices(conn, list)
		})
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
	return state.For(db).Write(context.Background(), hub.Event{Topic: "notices"}, func(conn *sql.Conn) error {
		list, err := readNotices(conn)
		if err != nil {
			return err
		}
		fn(&list)
		return saveNotices(conn, list)
	})
}
