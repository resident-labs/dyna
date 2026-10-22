package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/state"
)

const schemaVersion = 1

const schema = `CREATE TABLE library_entries (
	repo TEXT NOT NULL,
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	parent TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL,
	differs TEXT NOT NULL DEFAULT '',
	owns TEXT NOT NULL DEFAULT '[]',
	docs TEXT NOT NULL DEFAULT '[]',
	tests TEXT NOT NULL DEFAULT '[]',
	relates TEXT NOT NULL DEFAULT '[]',
	verified_at TEXT NOT NULL DEFAULT '',
	retired INTEGER NOT NULL DEFAULT 0,
	successor TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (repo, id)
);`

const columns = `id, name, kind, parent, summary, differs, owns, docs, tests, relates, verified_at, retired, successor`

// Store keeps feature maps for every repository in dyna.db. Repo keys are
// repoid IDs.
type Store struct {
	DataDir string
	// Actor is required for mutations. Read operations do not use it.
	Actor authority.Actor
}

// Migrate brings the library tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Store{DataDir: dataDir}.db()
	return err
}

func (s Store) db() (*sql.DB, error) {
	db, err := database.Open(s.DataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "library.version", schemaVersion, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func list(q querier, repo string) ([]Entry, error) {
	rows, err := q.QueryContext(context.Background(), `SELECT `+columns+` FROM library_entries WHERE repo=? ORDER BY id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var owns, docs, tests, relates string
		if err := rows.Scan(&e.ID, &e.Name, &e.Kind, &e.Parent, &e.Summary, &e.Differs, &owns, &docs, &tests, &relates, &e.VerifiedAt, &e.Retired, &e.Successor); err != nil {
			return nil, err
		}
		for _, f := range []struct {
			raw string
			to  *[]string
		}{{owns, &e.Owns}, {docs, &e.Docs}, {tests, &e.Tests}, {relates, &e.Relates}} {
			if err := json.Unmarshal([]byte(f.raw), f.to); err != nil {
				return nil, fmt.Errorf("library entry %s: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// List returns the repo's entries sorted by id. Retired entries are included
// only when all is set.
func (s Store) List(repo string, all bool) ([]Entry, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := list(db, repo)
	if err != nil || all {
		return rows, err
	}
	live := rows[:0]
	for _, e := range rows {
		if !e.Retired {
			live = append(live, e)
		}
	}
	return live, nil
}

// Get returns one entry, retired or not. A missing entry wraps os.ErrNotExist.
func (s Store) Get(repo, id string) (Entry, error) {
	rows, err := s.List(repo, true)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range rows {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("library entry %s: %w", id, os.ErrNotExist)
}

// Add creates an entry. Ids are stable, so a retired id is not reused.
func (s Store) Add(repo string, e Entry) (Entry, error) {
	if len(e.Owns) == 0 {
		return e, errors.New("a new entry needs at least one --owns glob")
	}
	return e, s.change("library add", repo, func(m map[string]*Entry) error {
		if m[e.ID] != nil {
			return fmt.Errorf("library entry %s already exists", e.ID)
		}
		m[e.ID] = &e
		return nil
	})
}

// Update changes one live entry through fn and returns the result.
func (s Store) Update(repo, id string, fn func(*Entry) error) (Entry, error) {
	var out Entry
	err := s.change("library update", repo, func(m map[string]*Entry) error {
		e := m[id]
		if e == nil || e.Retired {
			return fmt.Errorf("library entry %s: %w", id, os.ErrNotExist)
		}
		if err := fn(e); err != nil {
			return err
		}
		e.ID, out = id, *e
		return nil
	})
	return out, err
}

// Retire hides an entry. Its children move to the successor, or to the
// retired entry's own parent when there is no successor.
func (s Store) Retire(repo, id, successor string) (Entry, error) {
	var out Entry
	err := s.change("library retire", repo, func(m map[string]*Entry) error {
		e := m[id]
		if e == nil || e.Retired {
			return fmt.Errorf("library entry %s: %w", id, os.ErrNotExist)
		}
		if successor != "" && (successor == id || m[successor] == nil || m[successor].Retired) {
			return fmt.Errorf("successor %s is not a live entry", successor)
		}
		heir := successor
		if heir == "" {
			heir = e.Parent
		}
		for _, c := range m {
			if c.Retired {
				continue
			}
			if c.Parent == id {
				c.Parent = heir
			}
			// Relations follow the successor, or end with the entry.
			kept := c.Relates[:0]
			for _, r := range c.Relates {
				if r == id {
					r = successor
				}
				if r != "" && r != c.ID && !contains(kept, r) {
					kept = append(kept, r)
				}
			}
			c.Relates = kept
		}
		e.Retired, e.Successor = true, successor
		out = *e
		return nil
	})
	return out, err
}

// change loads the whole map in one writer transaction, applies fn, checks
// every live entry and the tree, and writes back only the rows that changed.
func (s Store) change(action, repo string, fn func(map[string]*Entry) error) error {
	if err := (librarianstate.Store{DataDir: s.DataDir}).CheckEdit(action, repo, s.Actor); err != nil {
		return err
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		rows, err := list(conn, repo)
		if err != nil {
			return err
		}
		m := make(map[string]*Entry, len(rows))
		before := make(map[string][]any, len(rows))
		for i := range rows {
			m[rows[i].ID] = &rows[i]
			before[rows[i].ID] = rowArgs(repo, rows[i])
		}
		if err := fn(m); err != nil {
			return err
		}
		if err := checkTree(m); err != nil {
			return err
		}
		ctx := context.Background()
		for id, e := range m {
			args := rowArgs(repo, *e)
			if old, ok := before[id]; ok && reflect.DeepEqual(old, args) {
				continue
			}
			if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO library_entries(repo, `+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// rowArgs lists the column values of one entry, repo first, in the order of columns.
func rowArgs(repo string, e Entry) []any {
	lists := make([]any, 0, 4)
	for _, l := range [][]string{e.Owns, e.Docs, e.Tests, e.Relates} {
		if l == nil {
			l = []string{}
		}
		data, _ := json.Marshal(l)
		lists = append(lists, string(data))
	}
	return append(append([]any{repo, e.ID, e.Name, e.Kind, e.Parent, e.Summary, e.Differs}, lists...), e.VerifiedAt, e.Retired, e.Successor)
}

// checkTree validates live entries, their parents and relations, and rejects
// a parent cycle.
func checkTree(m map[string]*Entry) error {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	live := func(id string) bool { return m[id] != nil && !m[id].Retired }
	var errs []error
	for _, id := range ids {
		e := m[id]
		if e.Retired {
			continue
		}
		if err := e.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
		if e.Parent != "" && !live(e.Parent) {
			errs = append(errs, fmt.Errorf("%s: parent %s is not a live entry", id, e.Parent))
		}
		for _, r := range e.Relates {
			if !live(r) || r == id {
				errs = append(errs, fmt.Errorf("%s: related entry %s is not another live entry", id, r))
			}
		}
		seen := map[string]bool{id: true}
		for p := e.Parent; p != "" && m[p] != nil; p = m[p].Parent {
			if seen[p] {
				errs = append(errs, fmt.Errorf("%s: parent chain has a cycle at %s", id, p))
				break
			}
			seen[p] = true
		}
	}
	return errors.Join(errs...)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
