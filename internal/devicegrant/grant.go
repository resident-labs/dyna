// Package devicegrant stores the scopes granted to a paired device or a
// Connect session in dyna.db. Token hashes stay in the JSON stores.
package devicegrant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"dyna-agent/internal/database"
	"dyna-agent/internal/scope"
)

const schema = `CREATE TABLE IF NOT EXISTS device_grants (
 kind TEXT NOT NULL,
 id TEXT NOT NULL,
 scopes TEXT NOT NULL,
 legacy INTEGER NOT NULL,
 PRIMARY KEY (kind, id)
);`

// legacyImported remembers a database whose one-time import has finished.
var legacyImported sync.Map // *sql.DB -> struct{}

// Grant is the access stored for one device.
type Grant struct {
	Kind   string
	ID     string
	Scopes []string
	Legacy bool
}

// Store reads and writes device_grants in one data directory.
type Store struct {
	db *sql.DB
}

// Migrate brings the device grant tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Open(dataDir)
	return err
}

// Open opens dyna.db in dataDir and creates the grants table.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("device grants need a data directory")
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "device_grants.version", 1, func(conn *sql.Conn, old int) error {
		if old != 0 {
			return nil
		}
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Put stores an explicit or legacy grant. It replaces any previous grant.
func (s *Store) Put(kind, id string, scopes []string, legacy bool) error {
	if err := checkKey(kind, id); err != nil {
		return err
	}
	scopes, err := scope.Normalize(scopes)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(scopes)
	if err != nil {
		return err
	}
	flag := 0
	if legacy {
		flag = 1
	}
	return database.Immediate(context.Background(), s.db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(),
			`INSERT INTO device_grants(kind, id, scopes, legacy) VALUES(?,?,?,?)
 ON CONFLICT(kind, id) DO UPDATE SET scopes=excluded.scopes, legacy=excluded.legacy`,
			kind, id, string(raw), flag)
		return err
	})
}

// Get returns the stored grant. The second result is false when there is none.
func (s *Store) Get(kind, id string) (Grant, bool, error) {
	if err := checkKey(kind, id); err != nil {
		return Grant{}, false, err
	}
	var raw string
	var legacy int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT scopes, legacy FROM device_grants WHERE kind=? AND id=?`, kind, id).Scan(&raw, &legacy)
	if err == sql.ErrNoRows {
		return Grant{}, false, nil
	}
	if err != nil {
		return Grant{}, false, err
	}
	g, err := decode(kind, id, raw, legacy)
	if err != nil {
		return Grant{}, false, err
	}
	return g, true, nil
}

// Map returns every grant of one kind, keyed by device id.
func (s *Store) Map(kind string) (map[string]Grant, error) {
	if kind != "paired" && kind != "connect" {
		return nil, fmt.Errorf("unknown device kind %q", kind)
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, scopes, legacy FROM device_grants WHERE kind=?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Grant{}
	for rows.Next() {
		var id, raw string
		var legacy int
		if err := rows.Scan(&id, &raw, &legacy); err != nil {
			return nil, err
		}
		g, err := decode(kind, id, raw, legacy)
		if err != nil {
			return nil, err
		}
		out[id] = g
	}
	return out, rows.Err()
}

// Delete removes grants for the ids. An empty list does nothing.
func (s *Store) Delete(kind string, ids []string) error {
	if kind != "paired" && kind != "connect" {
		return fmt.Errorf("unknown device kind %q", kind)
	}
	if len(ids) == 0 {
		return nil
	}
	return database.Immediate(context.Background(), s.db, func(conn *sql.Conn) error {
		for _, id := range ids {
			if id == "" {
				return fmt.Errorf("device id is required")
			}
			if _, err := conn.ExecContext(context.Background(),
				`DELETE FROM device_grants WHERE kind=? AND id=?`, kind, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: legacy/shim remove later — grants every device that existed before scopes full access and marks it legacy; remove after those devices are narrowed or revoked.
func (s *Store) ImportLegacy(paired, connect []string) error {
	if _, ok := legacyImported.Load(s.db); ok {
		return nil
	}
	err := database.Immediate(context.Background(), s.db, func(conn *sql.Conn) error {
		ctx := context.Background()
		var marker string
		err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='device_grants.legacy_imported'`).Scan(&marker)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		raw, err := json.Marshal(scope.All())
		if err != nil {
			return err
		}
		insert := func(kind string, ids []string) error {
			for _, id := range ids {
				if id == "" {
					continue
				}
				_, err := conn.ExecContext(ctx,
					`INSERT OR IGNORE INTO device_grants(kind, id, scopes, legacy) VALUES(?,?,?,1)`,
					kind, id, string(raw))
				if err != nil {
					return err
				}
			}
			return nil
		}
		if err := insert("paired", paired); err != nil {
			return err
		}
		if err := insert("connect", connect); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('device_grants.legacy_imported','1')`)
		return err
	})
	if err != nil {
		return err
	}
	legacyImported.Store(s.db, struct{}{})
	return nil
}

func checkKey(kind, id string) error {
	if kind != "paired" && kind != "connect" {
		return fmt.Errorf("unknown device kind %q", kind)
	}
	if id == "" {
		return fmt.Errorf("device id is required")
	}
	return nil
}

func decode(kind, id, raw string, legacy int) (Grant, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		return Grant{}, err
	}
	return Grant{Kind: kind, ID: id, Scopes: scopes, Legacy: legacy != 0}, nil
}
