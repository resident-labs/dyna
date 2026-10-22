// Package reposcope limits harness accounts to chosen repositories. An account
// with a list runs only in those repos. A repo named by any account is
// protected, so accounts without a list cannot run there. It also remembers
// which accounts hit a usage limit and until when. Both live in dyna.db.
package reposcope

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/state"
)

const schema = `CREATE TABLE IF NOT EXISTS account_repo_scopes (
 account_id TEXT NOT NULL,
 repo TEXT NOT NULL,
 PRIMARY KEY (account_id, repo)
);
CREATE TABLE IF NOT EXISTS account_limits (
 account_id TEXT PRIMARY KEY,
 until INTEGER NOT NULL
);`

// Store reads and writes account_repo_scopes in one data directory.
type Store struct {
	db *sql.DB
}

// Migrate brings the account scope tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := Open(dataDir)
	return err
}

// Open opens dyna.db in dataDir and creates the scope table.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("account scopes need a data directory")
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "account_repo_scopes.version", 2, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Set replaces the repo list of one account. An empty list removes the limit.
// Each repo is an identity from repoid.RemoteIdentity, such as github.com/org/name.
func (s *Store) Set(accountID string, repos []string) error {
	if accountID == "" {
		return errors.New("account scopes need an account ID")
	}
	for _, r := range repos {
		if r == "" || strings.ContainsAny(r, " \t\r\n") {
			return errors.New("a scope repo must be a repository identity such as github.com/org/name")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	// No topic: no consumer reads repo-scope events.
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "DELETE FROM account_repo_scopes WHERE account_id=?", accountID); err != nil {
			return err
		}
		for _, r := range repos {
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO account_repo_scopes(account_id, repo) VALUES(?,?)", accountID, r); err != nil {
				return err
			}
		}
		return nil
	})
}

// Identity turns what the owner typed, a remote URL or host/org/name, into the
// repository identity that the store keeps.
func Identity(input string) (string, error) {
	input = strings.TrimSpace(input)
	if strings.Contains(input, "://") || strings.Contains(input, "@") {
		return repoid.RemoteIdentity("", input)
	}
	host, rest, ok := strings.Cut(strings.Trim(strings.TrimSuffix(input, ".git"), "/"), "/")
	if !ok || !strings.Contains(host, ".") || strings.Count(rest, "/") < 1 || strings.ContainsAny(input, " \t") {
		return "", errors.New("A repo is a remote URL or host/org/name, such as github.com/org/name.")
	}
	return repoid.RemoteIdentity("", "https://"+host+"/"+rest)
}

// Rules is every account's repo list, keyed by account ID.
type Rules map[string][]string

// Rules reads all lists. Each list is sorted.
func (s *Store) Rules() (Rules, error) {
	return ReadRules(context.Background(), s.db)
}

// ReadRules reads all scope lists through the caller's database snapshot.
func ReadRules(ctx context.Context, q state.Reader) (Rules, error) {
	rows, err := q.QueryContext(ctx, "SELECT account_id, repo FROM account_repo_scopes ORDER BY account_id, repo")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := Rules{}
	for rows.Next() {
		var id, repo string
		if err := rows.Scan(&id, &repo); err != nil {
			return nil, err
		}
		rules[id] = append(rules[id], repo)
	}
	return rules, rows.Err()
}

// Protected reports whether any account names the repo.
func (r Rules) Protected(repo string) bool {
	for _, repos := range r {
		if slices.Contains(repos, repo) {
			return true
		}
	}
	return false
}

// Allows reports whether the account may run in the repo. An empty repo means
// no repository, where only accounts without a list may run.
func (r Rules) Allows(accountID, repo string) bool {
	if repos := r[accountID]; len(repos) > 0 {
		return repo != "" && slices.Contains(repos, repo)
	}
	return !r.Protected(repo)
}

// writeWait bounds a scope or limit write's wait for the writer, so a busy
// engine never holds an account switch without limit.
const writeWait = 10 * time.Second

// MarkLimited records that the account hit a usage limit that lasts until the given time.
// A shorter limit never replaces a longer one: the upsert keeps the later end.
func (s *Store) MarkLimited(accountID string, until time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx,
			`INSERT INTO account_limits(account_id, until) VALUES(?,?)
 ON CONFLICT(account_id) DO UPDATE SET until=MAX(until, excluded.until)`, accountID, until.Unix())
		return err
	})
}

// Forget deletes an account's repo list and limit inside the caller's
// write, the account store's removal.
func Forget(ctx context.Context, conn *sql.Conn, accountID string) error {
	for _, table := range []string{"account_repo_scopes", "account_limits"} {
		ok, err := database.HasTable(ctx, conn, table)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// The table is one of two constants, never input.
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE account_id = ?", accountID); err != nil {
			return err
		}
	}
	return nil
}

// Limits returns each account's limit end that is still after now.
func (s *Store) Limits(now time.Time) (map[string]time.Time, error) {
	rows, err := s.db.Query("SELECT account_id, until FROM account_limits WHERE until>?", now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	limits := map[string]time.Time{}
	for rows.Next() {
		var id string
		var until int64
		if err := rows.Scan(&id, &until); err != nil {
			return nil, err
		}
		limits[id] = time.Unix(until, 0)
	}
	return limits, rows.Err()
}
