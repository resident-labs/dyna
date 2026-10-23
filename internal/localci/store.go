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
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
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

// Node names a machine that runs CI checks. Empty SSH runs on this machine.
type Node struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh,omitempty"`
	Slots   int    `json:"slots"`
	Enabled bool   `json:"enabled"`
}

type Store struct{ DataDir string }

// HostedRuns records the hosted attempts cancelled for a local run.
type HostedRuns struct {
	IDs          []int64       `json:"ids"`
	Attempts     map[int64]int `json:"attempts,omitempty"`
	Resumed      bool          `json:"resumed"`
	Published    bool          `json:"published"`
	PublishedKey string        `json:"publishedKey,omitempty"`
}

const storeSchema = `CREATE TABLE local_ci_modes (
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

// cacheCleanSchema records when Dyna last cleaned a CI machine's own caches.
const cacheCleanSchema = `CREATE TABLE IF NOT EXISTS local_ci_cache_cleans (
 node TEXT PRIMARY KEY,
 cleaned_at TEXT NOT NULL
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
	err = database.Migrate(db, "localci.schema", 3, func(conn *sql.Conn, old int) error {
		if old == 1 {
			// Version 1 had a jump host column; nodes are now reached directly.
			// The machines migration drops this table, so it may be gone already.
			present, err := database.HasTable(context.Background(), conn, "local_ci_nodes")
			if err != nil {
				return err
			}
			if present {
				if _, err := conn.ExecContext(context.Background(), "ALTER TABLE local_ci_nodes DROP COLUMN jump"); err != nil {
					return err
				}
			}
		}
		if old == 0 {
			if _, err := conn.ExecContext(context.Background(), storeSchema); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(context.Background(), cacheCleanSchema)
		return err
	})
	return db, err
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// Validate reports whether a runner can use this node.
func (n Node) Validate() error {
	if err := machines.ValidateName(n.Name); err != nil {
		return err
	}
	if n.Slots < 1 {
		return errors.New("CI node slots must be positive")
	}
	if n.SSH != "" {
		return machines.ValidateSSH(n.SSH)
	}
	return nil
}

// Nodes returns the machines with the ci role, as the scheduler reads them.
// The engine host maps to an empty SSH destination, which runs on this machine.
func (s Store) Nodes(ctx context.Context) ([]Node, error) {
	list, err := s.machines().List(ctx)
	if err != nil {
		return nil, err
	}
	nodes := []Node{}
	for _, m := range list {
		if !m.HasRole(machines.RoleCI) {
			continue
		}
		nodes = append(nodes, Node{Name: m.Name, SSH: m.SSH, Slots: m.CISlots, Enabled: m.Enabled})
	}
	return nodes, nil
}

// machines is the registry that holds the CI machines.
func (s Store) machines() machines.Store { return machines.Store{DataDir: s.root()} }

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

// LastCacheClean reports when Dyna last cleaned this node's caches. A node
// that was never cleaned has the zero time.
func (s Store) LastCacheClean(ctx context.Context, node string) (time.Time, error) {
	db, err := s.open()
	if err != nil {
		return time.Time{}, err
	}
	var raw string
	err = db.QueryRowContext(ctx, "SELECT cleaned_at FROM local_ci_cache_cleans WHERE node=?", node).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("read cache cleanup time of %s: %w", node, err)
	}
	return at, nil
}

func (s Store) SaveCacheClean(ctx context.Context, node string, at time.Time) error {
	db, err := s.open()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{Topic: Topic, Key: "caches:" + node}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO local_ci_cache_cleans(node,cleaned_at) VALUES(?,?)
ON CONFLICT(node) DO UPDATE SET cleaned_at=excluded.cleaned_at`, node, at.UTC().Format(time.RFC3339Nano))
		return err
	})
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
