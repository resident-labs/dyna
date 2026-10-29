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
 until INTEGER NOT NULL,
 stated INTEGER NOT NULL DEFAULT 0,
 recorded_at INTEGER NOT NULL DEFAULT 0,
 backoff INTEGER NOT NULL DEFAULT 0
);`

// OpenEnded is the end of a hold with no timer. A credit failure (402) holds
// until a later account refresh or health probe clears it.
var OpenEnded = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// Hold is one account's usage-limit hold. Level counts the failures that
// stepped the backoff, so the next unstated hold waits longer.
type Hold struct {
	Until  time.Time
	Stated bool
	Level  int
}

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
	err = database.Migrate(db, "account_repo_scopes.version", 5, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		if old == 2 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE account_limits ADD COLUMN stated INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
		if old >= 2 && old < 5 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE account_limits ADD COLUMN backoff INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
		if old == 2 || old == 3 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE account_limits ADD COLUMN recorded_at INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
			// A reading must be newer than the migration to refute an old hold.
			_, err := conn.ExecContext(ctx, `UPDATE account_limits SET recorded_at=? WHERE recorded_at=0`, time.Now().UnixNano())
			return err
		}
		return nil
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
// The end is a guess; MarkReset records one the provider stated.
func (s *Store) MarkLimited(accountID string, until time.Time) error {
	return s.Hold(accountID, Hold{Until: until})
}

// MarkReset is MarkLimited for an end the provider stated, so Stated reports it.
func (s *Store) MarkReset(accountID string, reset time.Time) error {
	return s.Hold(accountID, Hold{Until: reset, Stated: true})
}

// Hold records a limit hold. The later end wins, and its stated flag and level go with it.
func (s *Store) Hold(accountID string, h Hold) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx,
			`INSERT INTO account_limits(account_id, until, stated, recorded_at, backoff) VALUES(?,?,?,?,?)
 ON CONFLICT(account_id) DO UPDATE SET stated=CASE WHEN excluded.until>=until THEN excluded.stated ELSE stated END, backoff=CASE WHEN excluded.until>=until THEN excluded.backoff ELSE backoff END, until=MAX(until, excluded.until), recorded_at=excluded.recorded_at`, accountID, h.Until.Unix(), h.Stated, time.Now().UnixNano(), h.Level)
		return err
	})
}

// LimitOf returns the account's stored hold, ended or not. A missing row is the zero Hold.
func (s *Store) LimitOf(accountID string) (Hold, error) {
	var until, backoff int64
	var stated bool
	err := s.db.QueryRow("SELECT until, stated, backoff FROM account_limits WHERE account_id=?", accountID).Scan(&until, &stated, &backoff)
	if errors.Is(err, sql.ErrNoRows) {
		return Hold{}, nil
	}
	if err != nil {
		return Hold{}, err
	}
	return Hold{Until: time.Unix(until, 0), Stated: stated, Level: int(backoff)}, nil
}

// ClearLimit removes the account's hold, because a turn or probe on it succeeded.
func (s *Store) ClearLimit(accountID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM account_limits WHERE account_id=?", accountID)
		return err
	})
}

// ClearOpenEnded removes the account's hold only when it has no reset time, so
// a successful probe or refresh ends a credits hold and leaves timed holds alone.
func (s *Store) ClearOpenEnded(accountID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	return state.For(s.db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM account_limits WHERE account_id=? AND until=?", accountID, OpenEnded.Unix())
		return err
	})
}

// Stated returns the limit ends that the provider stated and that are still
// after now. A limit end not in it is a guess.
func (s *Store) Stated(now time.Time) (map[string]time.Time, error) {
	rows, err := s.db.Query("SELECT account_id, until FROM account_limits WHERE until>? AND stated=1", now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var until int64
		if err := rows.Scan(&id, &until); err != nil {
			return nil, err
		}
		out[id] = time.Unix(until, 0)
	}
	return out, rows.Err()
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

// LimitRecordedAt returns when each still-active limit recorded its failure.
// A healthy usage reading must be newer to refute that failure.
func (s *Store) LimitRecordedAt(now time.Time) (map[string]time.Time, error) {
	rows, err := s.db.Query("SELECT account_id, recorded_at FROM account_limits WHERE until>?", now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recorded := map[string]time.Time{}
	for rows.Next() {
		var id string
		var nanos int64
		if err := rows.Scan(&id, &nanos); err != nil {
			return nil, err
		}
		if nanos > 0 {
			recorded[id] = time.Unix(0, nanos)
		}
	}
	return recorded, rows.Err()
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
