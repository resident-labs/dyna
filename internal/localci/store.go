package localci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const (
	ModeGitHub = "github"
	ModeLocal  = "local"
	ModeBoth   = "both"
	Topic      = "localci"
)

// Node names a local or SSH runner. Empty SSH runs on this machine.
type Node struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh,omitempty"`
	Jump    string `json:"jump,omitempty"`
	Slots   int    `json:"slots"`
	Enabled bool   `json:"enabled"`
}

type Store struct{ DataDir string }

// HostedRuns records the hosted attempts cancelled for a local run.
type HostedRuns struct {
	IDs       []int64       `json:"ids"`
	Attempts  map[int64]int `json:"attempts,omitempty"`
	Resumed   bool          `json:"resumed"`
	Published bool          `json:"published"`
}

const storeSchema = `CREATE TABLE local_ci_nodes (
 name TEXT PRIMARY KEY,
 ssh TEXT NOT NULL,
 jump TEXT NOT NULL,
 slots INTEGER NOT NULL CHECK(slots > 0),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1))
);
CREATE TABLE local_ci_modes (
 room_id TEXT PRIMARY KEY,
 mode TEXT NOT NULL CHECK(mode IN ('github','local','both'))
);
CREATE TABLE local_ci_results (
 repo TEXT NOT NULL,
 sha TEXT NOT NULL,
 payload BLOB NOT NULL CHECK(json_valid(payload)),
 PRIMARY KEY(repo,sha)
);
CREATE TABLE local_ci_hosted_runs (
 repo TEXT NOT NULL,
 sha TEXT NOT NULL,
 payload BLOB NOT NULL CHECK(json_valid(payload)),
 PRIMARY KEY(repo,sha)
);`

// Migrate prepares the local CI tables at engine startup.
func Migrate(dataDir string) error {
	_, err := (Store{DataDir: dataDir}).open()
	return err
}

func (s Store) root() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	return store.DataDir()
}

func (s Store) open() (*sql.DB, error) {
	db, err := database.Open(s.root())
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "localci.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), storeSchema)
		return err
	})
	return db, err
}

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var sshTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*(@[a-zA-Z0-9][a-zA-Z0-9_.-]*)?$`)
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func (n Node) Validate() error {
	if !nodeName.MatchString(n.Name) {
		return fmt.Errorf("node name must contain only letters, numbers, dots, underscores or hyphens, and start with a letter or number")
	}
	if n.Slots < 1 {
		return fmt.Errorf("node slots must be positive")
	}
	for _, target := range []struct{ name, value string }{{"SSH", n.SSH}, {"jump", n.Jump}} {
		if target.value != "" && !sshTarget.MatchString(target.value) {
			return fmt.Errorf("%s destination must be a host or user@host", target.name)
		}
	}
	if n.Jump != "" && n.SSH == "" {
		return fmt.Errorf("a jump destination requires an SSH destination")
	}
	return nil
}

func (s Store) Nodes(ctx context.Context) ([]Node, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT name,ssh,jump,slots,enabled FROM local_ci_nodes ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.Name, &n.SSH, &n.Jump, &n.Slots, &n.Enabled); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s Store) PutNode(ctx context.Context, n Node) error {
	if err := n.Validate(); err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "node:" + n.Name}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO local_ci_nodes(name,ssh,jump,slots,enabled) VALUES(?,?,?,?,?)
ON CONFLICT(name) DO UPDATE SET ssh=excluded.ssh,jump=excluded.jump,slots=excluded.slots,enabled=excluded.enabled`, n.Name, n.SSH, n.Jump, n.Slots, n.Enabled)
		return err
	})
}

func (s Store) RemoveNode(ctx context.Context, name string) error {
	return s.changeNode(ctx, name, "DELETE FROM local_ci_nodes WHERE name=?")
}

func (s Store) DisableNode(ctx context.Context, name string) error {
	return s.changeNode(ctx, name, "UPDATE local_ci_nodes SET enabled=0 WHERE name=?")
}

func (s Store) changeNode(ctx context.Context, name, query string) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "node:" + name}, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, query, name)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count == 0 {
			return fmt.Errorf("CI node %q: %w", name, os.ErrNotExist)
		}
		return err
	})
}

func ValidateMode(mode string) error {
	switch mode {
	case ModeGitHub, ModeLocal, ModeBoth:
		return nil
	default:
		return fmt.Errorf("CI mode must be github, local or both")
	}
}

func (s Store) Mode(ctx context.Context, roomID string) (string, error) {
	if strings.TrimSpace(roomID) == "" {
		return "", fmt.Errorf("CI mode requires a room ID")
	}
	db, err := s.open()
	if err != nil {
		return "", err
	}
	var mode string
	err = db.QueryRowContext(ctx, "SELECT mode FROM local_ci_modes WHERE room_id=?", roomID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return ModeGitHub, nil
	}
	return mode, err
}

func (s Store) SetMode(ctx context.Context, roomID, mode string) error {
	if strings.TrimSpace(roomID) == "" {
		return fmt.Errorf("CI mode requires a room ID")
	}
	if err := ValidateMode(mode); err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Room: roomID, Key: "mode"}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO local_ci_modes(room_id,mode) VALUES(?,?)
ON CONFLICT(room_id) DO UPDATE SET mode=excluded.mode`, roomID, mode)
		return err
	})
}

// CanonicalRepo gives every worktree the same result key.
func CanonicalRepo(ctx context.Context, repo string) (string, error) {
	common, err := repoid.Git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return canonicalKey(common)
}

// Store callers pass the canonical git common directory, not a checkout.
func canonicalKey(repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", fmt.Errorf("CI result requires a repository")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if os.IsNotExist(err) {
		return abs, nil // Results remain readable after a repository is removed.
	}
	return canonical, err
}

func resultKey(repo, sha string) (string, error) {
	if !commitSHA.MatchString(sha) {
		return "", fmt.Errorf("expected a full commit SHA")
	}
	return canonicalKey(repo)
}

func (s Store) SaveResult(ctx context.Context, result Result) error {
	repo, err := resultKey(result.Repo, result.SHA)
	if err != nil {
		return err
	}
	result.Repo = repo
	return s.savePayload(ctx, "local_ci_results", repo, result.SHA, result)
}

func (s Store) LoadResult(ctx context.Context, repo, sha string) (Result, error) {
	var result Result
	repo, err := resultKey(repo, sha)
	if err != nil {
		return result, err
	}
	err = s.loadPayload(ctx, "local_ci_results", repo, sha, &result)
	if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	// TODO: legacy/shim remove later — import the previous runner's result.json
	// once per repository and commit. SQLite remains authoritative afterwards.
	legacy := filepath.Join(s.root(), "local-ci", repoKey(repo), sha, "result.json")
	raw, err := os.ReadFile(legacy)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("import local CI result %s: %w", legacy, err)
	}
	if result.SHA != sha {
		return Result{}, fmt.Errorf("legacy CI result SHA does not match %s", sha)
	}
	result.Repo = repo
	raw, err = json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	db, err := s.open()
	if err != nil {
		return Result{}, err
	}
	err = state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "result:" + sha}, func(conn *sql.Conn) error {
		// A runner may have saved a newer result after our initial read.
		_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO local_ci_results(repo,sha,payload) VALUES(?,?,?)", repo, sha, raw)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	err = s.loadPayload(ctx, "local_ci_results", repo, sha, &result)
	return result, err
}

func (s Store) SaveHostedRuns(ctx context.Context, repo, sha string, runs HostedRuns) error {
	repo, err := resultKey(repo, sha)
	if err != nil {
		return err
	}
	return s.savePayload(ctx, "local_ci_hosted_runs", repo, sha, runs)
}

func (s Store) LoadHostedRuns(ctx context.Context, repo, sha string) (HostedRuns, error) {
	var runs HostedRuns
	repo, err := resultKey(repo, sha)
	if err != nil {
		return runs, err
	}
	err = s.loadPayload(ctx, "local_ci_hosted_runs", repo, sha, &runs)
	return runs, err
}

func (s Store) savePayload(ctx context.Context, table, repo, sha string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	key := "result:" + sha
	if table == "local_ci_hosted_runs" {
		key = "hosted:" + sha
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: key}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "INSERT INTO "+table+"(repo,sha,payload) VALUES(?,?,?) ON CONFLICT(repo,sha) DO UPDATE SET payload=excluded.payload", repo, sha, raw)
		return err
	})
}

func (s Store) loadPayload(ctx context.Context, table, repo, sha string, target any) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, "SELECT payload FROM "+table+" WHERE repo=? AND sha=?", repo, sha).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return os.ErrNotExist
		}
		return err
	}
	return json.Unmarshal(raw, target)
}
