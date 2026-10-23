package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
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
 facts TEXT NOT NULL DEFAULT '',
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
	err = database.Migrate(db, "notices.schema", 3, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old == 0 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		if old > 0 && old < 3 {
			// The typed facts of a notice live in one JSON column, decoded by the
			// notice's kind. A notice saved before them keeps an empty column and
			// shows its stored line.
			has, err := hasColumn(conn, "notices", "facts")
			if err != nil {
				return err
			}
			if !has {
				if _, err := conn.ExecContext(ctx, "ALTER TABLE notices ADD COLUMN facts TEXT NOT NULL DEFAULT ''"); err != nil {
					return err
				}
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

// hasColumn reports whether a table already has a column, so a migration that
// adds it can run over a table a test created in the new shape.
func hasColumn(conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func readNotices(q state.Reader) ([]Notice, error) {
	rows, err := q.QueryContext(context.Background(), "SELECT id,key,kind,severity,text,command,facts,at FROM notices ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Notice{}
	for rows.Next() {
		var n Notice
		var at, facts string
		if err := rows.Scan(&n.ID, &n.Key, &n.Kind, &n.Severity, &n.Text, &n.Command, &facts, &at); err != nil {
			return nil, err
		}
		if n.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		if err := decodeFacts(&n, facts); err != nil {
			return nil, err
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

// decodeFacts reads the stored facts into the typed field of the notice's
// kind. An empty column is a notice saved before the typed facts.
func decodeFacts(n *Notice, facts string) error {
	if facts == "" {
		return nil
	}
	switch n.Kind {
	case KindDisk:
		var f DiskFacts
		if err := json.Unmarshal([]byte(facts), &f); err != nil {
			return fmt.Errorf("notice %s disk facts: %w", n.ID, err)
		}
		n.Disk = &f
	case KindProvider:
		var f ProviderFacts
		if err := json.Unmarshal([]byte(facts), &f); err != nil {
			return fmt.Errorf("notice %s provider facts: %w", n.ID, err)
		}
		n.Provider = &f
	case KindSync:
		var f SyncFacts
		if err := json.Unmarshal([]byte(facts), &f); err != nil {
			return fmt.Errorf("notice %s sync facts: %w", n.ID, err)
		}
		n.Sync = &f
	}
	return nil
}

// encodeFacts writes the typed facts of the notice's kind as JSON.
func encodeFacts(n Notice) (string, error) {
	var facts any
	switch {
	case n.Disk != nil:
		facts = n.Disk
	case n.Provider != nil:
		facts = n.Provider
	case n.Sync != nil:
		facts = n.Sync
	default:
		return "", nil
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
		if j, ok := before[n.ID]; ok && i == j && reflect.DeepEqual(old[j], n) {
			continue
		}
		facts, err := encodeFacts(n)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO notices(id,position,key,kind,severity,text,command,facts,at) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET position=excluded.position,key=excluded.key,kind=excluded.kind,severity=excluded.severity,text=excluded.text,command=excluded.command,facts=excluded.facts,at=excluded.at`, n.ID, i, n.Key, n.Kind, n.Severity, n.Text, n.Command, facts, n.At.UTC().Format(time.RFC3339Nano)); err != nil {
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
