// Package worktrees owns Dyna's registry of retained temporary git worktrees
// and the safety checks used to inspect and remove them.
package worktrees

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const (
	StateMissing   = "missing"
	StateUnchanged = "unchanged"
	StateModified  = "modified"
	StateError     = "error"
)

// Record is engine-written metadata for one retained worktree.
type Record struct {
	Root       string    `json:"root,omitempty"`
	Session    string    `json:"session,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	CreatedAt  time.Time `json:"createdAt,omitempty"`
	Path       string    `json:"path"`
	RunID      string    `json:"runId"`
	AgentID    int       `json:"agentId"`
	Label      string    `json:"label"`
	Profile    string    `json:"profile"`
	Repo       string    `json:"repo"`
	Base       string    `json:"base"`
	Head       string    `json:"head"`
	Port       int       `json:"port,omitempty"` // first port of the 10-port block
	Status     string    `json:"status"`
	RetainedAt time.Time `json:"retainedAt"`
}

// Disposition controls what happens to registered worktrees during run deletion.
type Disposition string

const (
	DispositionRetain          Disposition = "retain"
	DispositionRemoveUnchanged Disposition = "remove-unchanged"
	DispositionRemoveAll       Disposition = "remove-all"
)

// ParseDisposition validates a run-deletion worktree disposition.
func ParseDisposition(value string) (Disposition, error) {
	disposition := Disposition(strings.ToLower(strings.TrimSpace(value)))
	switch disposition {
	case DispositionRetain, DispositionRemoveUnchanged, DispositionRemoveAll:
		return disposition, nil
	default:
		return "", fmt.Errorf("worktree disposition must be one of retain, remove-unchanged, remove-all")
	}
}

const (
	ActionRetained = "retained"
	ActionRemoved  = "removed"
	ActionSkipped  = "skipped"
)

// Action describes one ApplyRunDisposition decision.
type Action struct {
	Record  Record `json:"record"`
	Path    string `json:"path"`
	State   string `json:"state,omitempty"`
	Action  string `json:"action"`
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

func validatePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("worktree path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	abs = filepath.Clean(abs)
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve temporary directory: %w", err)
	}
	tmp = filepath.Clean(tmp)
	base := filepath.Base(abs)
	if managedPath(abs) {
		return abs, nil
	}
	if filepath.Dir(abs) != tmp || !strings.HasPrefix(base, "dyna-wt-") || len(base) == len("dyna-wt-") {
		return "", fmt.Errorf("refusing non-Dyna temporary worktree path %q", path)
	}
	return abs, nil
}

// recordKey returns the absolute, clean path that keys a registry record.
func recordKey(path string) (string, error) {
	safe, err := filepath.Abs(path)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("invalid registry path %q", path)
	}
	return filepath.Clean(safe), nil
}

var errPortClaimed = errors.New("worktree port block was claimed")

// Track creates or replaces the registry record for a retained worktree.
// Each update commits in one database write transaction. A concurrent port
// claim retries allocation before the commit, so calls never share a block.
func Track(rec Record) error {
	if rec.Root == "" {
		if old, err := Get(rec.Path); err == nil {
			rec.Root = old.Root
		}
	}
	safe, err := validateRecordPath(rec)
	if err != nil {
		return err
	}
	rec.Path = safe
	if rec.Root == "" && managedPath(safe) {
		rec.Root, _ = ManagedRoot()
	}
	db, err := registryDB()
	if err != nil {
		return err
	}
	if rec.RunID != "" {
		if err := runstore.Migrate(store.DataDir()); err != nil {
			return err
		}
	}
	ctx := context.Background()
	// Each conflict reloads committed claims, so retries skip occupied blocks
	// and converge on a free block or the allocation limit.
	for {
		port := rec.Port
		if port == 0 {
			records, err := loadRecords(ctx, db, "")
			if err != nil {
				return err
			}
			for _, old := range records {
				if old.Path == safe {
					port = old.Port
					break
				}
			}
			if port == 0 {
				// Socket probes run before the writer transaction. The transaction
				// checks the chosen block again against current registry records.
				port, err = allocatePort(safe, records, portFree)
				if err != nil {
					// The pool is full. Free one block from a tree that is safe to
					// drop, then allocate again from the committed records.
					if reclaimPortBlock(ctx, db, records, safe) {
						continue
					}
					return fmt.Errorf("allocate worktree port: %w", err)
				}
			}
		}
		// Worktree records have no change-bus consumer.
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			// Workflow retention updates must preserve the original owner, creation time and port.
			if old, err := loadRecords(ctx, conn, "WHERE path=?", safe); err != nil {
				return err
			} else if len(old) == 1 {
				if rec.Session == "" {
					rec.Session = old[0].Session
				}
				if rec.Root == "" {
					rec.Root = old[0].Root
				}
				if rec.Branch == "" {
					rec.Branch = old[0].Branch
				}
				if rec.CreatedAt.IsZero() {
					rec.CreatedAt = old[0].CreatedAt
				}
				if rec.Port == 0 {
					rec.Port = old[0].Port
				}
			}

			if rec.RunID != "" {
				run, err := runstore.LoadStored(ctx, conn, rec.RunID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil && run.OwnerHandoff {
					rec.Session = run.ParentSession
					if rec.Session == "" {
						rec.Session = run.Session
					}
				}
			}
			if rec.Port == 0 {
				records, err := loadRecords(ctx, conn, "")
				if err != nil {
					return err
				}
				for _, old := range records {
					if old.Path != safe && old.Port == port {
						return errPortClaimed
					}
				}
				rec.Port = port
			}
			return saveRecord(ctx, conn, rec)
		})
		if !errors.Is(err, errPortClaimed) {
			return err
		}
	}
}

// List returns every retained worktree record in deterministic path order.
func List() ([]Record, error) {
	records, _, err := ListWithWarnings()
	return records, err
}

// ListWithWarnings is List. It returns no warnings, because the database rejects damaged rows.
func ListWithWarnings() ([]Record, []error, error) {
	db, err := registryDB()
	if err != nil {
		return nil, nil, err
	}
	records, err := loadRecords(context.Background(), db, "")
	return records, nil, err
}

// KeepSession locks every worktree that cleanup selects for session, so the
// close-time cleanup and the scheduled maintenance pass keep them. A manual
// handoff needs the source trees after the source closes. It returns the paths
// it locked and an error for any tree it could not keep, so the caller stops
// before a cleanup removes what it meant to keep. Only a proven absent entry
// is skipped.
//
// The selection is cleanup's own: the registry records this session owns and,
// through sessionWorktreePaths, the linked tree that holds the session's
// directory. A session can work in a tree the registry still names after its
// earlier owner, and cleanup removes that tree once both owners are closed.
func KeepSession(ctx context.Context, session, reason string) ([]string, error) {
	paths, errs := sessionTreePaths(ctx, session)
	var kept []string
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("inspect worktree %s: %w", path, err))
			continue
		}
		if err := Lock(ctx, path, reason); err != nil {
			errs = append(errs, err)
			continue
		}
		kept = append(kept, path)
	}
	return kept, errors.Join(errs...)
}

// sessionTreePaths returns every path cleanup could select for session: its
// registry records, and the linked trees of the repository that holds the
// session's directory.
func sessionTreePaths(ctx context.Context, session string) ([]string, []error) {
	records, err := List()
	if err != nil {
		return nil, []error{err}
	}
	var errs []error
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[filepath.Clean(path)] {
			return
		}
		seen[filepath.Clean(path)] = true
		paths = append(paths, path)
	}
	for _, rec := range records {
		if rec.Session == session {
			add(rec.Path)
		}
	}
	trees, err := sessionRepositoryTrees(ctx, session)
	if err != nil {
		errs = append(errs, err)
	}
	for _, tree := range trees {
		add(tree.Path)
	}
	return paths, errs
}

// sessionRepositoryTrees runs cleanup's selection for the repository that holds
// the session's directory. It reads no remote and removes nothing.
func sessionRepositoryTrees(ctx context.Context, session string) ([]Tree, error) {
	var meta struct{ ID, Dir string }
	if err := sessionlife.Load(runstore.SessionsDir(), session, &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session %s for its worktrees: %w", session, err)
	}
	if meta.Dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(meta.Dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect session directory %s: %w", meta.Dir, err)
	}
	out, err := gitOutput(ctx, meta.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		// A directory outside a repository has no linked trees to keep.
		if strings.Contains(string(out), "not a git repository") {
			return nil, nil
		}
		return nil, fmt.Errorf("find the repository of %s: %w: %s", meta.Dir, err, strings.TrimSpace(string(out)))
	}
	repo := strings.TrimSpace(string(out))
	trees, err := scan(ctx, repo, &GCOptions{Session: session, history: &runHistory{}})
	if err != nil {
		return nil, fmt.Errorf("select the session's worktrees: %w", err)
	}
	return trees, nil
}

// Lock marks the worktree at path as locked, which cleanup reads and keeps. A
// worktree that is already locked keeps its first reason.
func Lock(ctx context.Context, path, reason string) error {
	out, err := gitOutput(ctx, path, "worktree", "lock", "--reason", reason, path)
	if err != nil && !strings.Contains(string(out), "is already locked") {
		return fmt.Errorf("lock worktree %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ForRun returns the retained worktrees attributed to runID.
func ForRun(runID string) ([]Record, error) {
	records, _, err := ForRunWithWarnings(runID)
	return records, err
}

// ForRunWithWarnings is ForRun plus diagnostics for skipped registry records.
func ForRunWithWarnings(runID string) ([]Record, []error, error) {
	db, err := registryDB()
	if err != nil {
		return nil, nil, err
	}
	records, err := loadRecords(context.Background(), db, "WHERE run_id=?", runID)
	return records, nil, err
}

// Get resolves a registered worktree by its dyna-wt-* basename or exact path.
func Get(nameOrPath string) (Record, error) {
	records, warnings, err := ListWithWarnings()
	if err != nil {
		return Record{}, err
	}
	var matches []Record
	for _, rec := range records {
		if rec.Path == nameOrPath {
			return rec, nil
		}
		if filepath.Base(rec.Path) == nameOrPath {
			matches = append(matches, rec)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Record{}, fmt.Errorf("worktree name %q is ambiguous; use an exact path", nameOrPath)
	}
	return Record{}, fmt.Errorf("worktree %q is not registered (%d registry warnings)", nameOrPath, len(warnings))
}

// MarkRemoved deletes the registry record of a removed tree, but only the
// version it is given: a path recreated and tracked after the removal keeps
// its new record and its port. It is idempotent.
func MarkRemoved(rec Record) error {
	key, err := recordKey(rec.Path)
	if err != nil {
		return err
	}
	rec.Path = key
	db, err := registryDB()
	if err != nil {
		return err
	}
	// Worktree records have no change-bus consumer.
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		if err := removeObservedRecord(context.Background(), conn, rec); err != nil {
			return err
		}
		return rearmPortCapacity(context.Background(), conn)
	})
}

// trackedRecord reads the record of an exact path. found is false when no
// record exists for the path; an error means the registry could not be read,
// which callers must treat as unknown, never as absent. Tests replace it.
var trackedRecord = func(path string) (rec Record, found bool, err error) {
	records, _, err := ListWithWarnings()
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range records {
		if r.Path == path {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Inspect compares a retained worktree's current HEAD and porcelain status to
// the engine-captured retention-time fingerprint.
func Inspect(rec Record) (string, error) {
	safe, err := validateRecordPath(rec)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(safe); err != nil {
		if os.IsNotExist(err) {
			return StateMissing, nil
		}
		return "", fmt.Errorf("stat worktree: %w", err)
	}
	head, status, _, err := Fingerprint(context.Background(), safe, rec.Head)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(head) == strings.TrimSpace(rec.Head) && normalizeStatus([]byte(status)) == normalizeStatus([]byte(rec.Status)) {
		return StateUnchanged, nil
	}
	return StateModified, nil
}

// ModifiedSinceRetentionError describes the current differences from a
// worktree's retention-time fingerprint.
func ModifiedSinceRetentionError(rec Record) error {
	detail := "HEAD or working tree status changed"
	head, status, _, err := Fingerprint(context.Background(), rec.Path, rec.Head)
	if err == nil {
		var changes []string
		if strings.TrimSpace(head) != strings.TrimSpace(rec.Head) {
			previous := strings.TrimSpace(rec.Head)
			if previous == "" {
				previous = "<unrecorded>"
			}
			changes = append(changes, fmt.Sprintf("HEAD changed from %s to %s", previous, head))
		}
		currentStatus := normalizeStatus([]byte(status))
		retainedStatus := normalizeStatus([]byte(rec.Status))
		if currentStatus != retainedStatus {
			if currentStatus == "" {
				changes = append(changes, fmt.Sprintf("working tree status changed from %q to clean", retainedStatus))
			} else {
				changes = append(changes, fmt.Sprintf("working tree status is now %q", currentStatus))
			}
		}
		if len(changes) > 0 {
			detail = strings.Join(changes, "; ")
		}
	}
	return fmt.Errorf("worktree %s is modified since retention: %s; use --force to remove it", rec.Path, detail)
}

// Remove safely removes a registry-backed worktree. Unless force is true, its
// retention-time HEAD and status must still match exactly.
func Remove(rec Record, force bool) error {
	registered, err := Get(rec.Path)
	if err != nil {
		return err
	}
	state, err := Inspect(registered)
	if err != nil {
		if !force {
			return err
		}
		state = StateModified
	}
	if state == StateModified && !force {
		return ModifiedSinceRetentionError(registered)
	}
	if strings.TrimSpace(registered.Repo) == "" {
		return fmt.Errorf("worktree %s has no recorded source repository", registered.Path)
	}
	if _, err := os.Stat(registered.Repo); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat source repository: %w", err)
		}
		if err := os.RemoveAll(registered.Path); err != nil {
			return fmt.Errorf("remove orphaned worktree: %w", err)
		}
		return MarkRemoved(registered)
	}

	listed, err := repoHasWorktree(registered.Repo, registered.Path)
	if err != nil {
		return err
	}
	if !listed {
		if state == StateMissing {
			return MarkRemoved(registered)
		}
		return fmt.Errorf("refusing to remove %s: it is not listed by source repository %s", registered.Path, registered.Repo)
	}
	if out, err := gitOutput(context.Background(), registered.Repo, "worktree", "remove", "--force", registered.Path); err != nil {
		return fmt.Errorf("remove git worktree %s: %v: %s", registered.Path, err, strings.TrimSpace(string(out)))
	}
	return MarkRemoved(registered)
}

// ApplyRunDisposition applies one explicit run-deletion disposition to all
// registry records attributed to runID. Modified trees are skipped by
// remove-unchanged.
func ApplyRunDisposition(runID string, disposition Disposition) ([]Action, error) {
	actions, _, err := ApplyRunDispositionWithWarnings(runID, disposition)
	return actions, err
}

// ApplyRunDispositionWithWarnings is ApplyRunDisposition plus diagnostics for skipped
// registry records and worktrees that could not be inspected.
func ApplyRunDispositionWithWarnings(runID string, disposition Disposition) ([]Action, []error, error) {
	d, err := ParseDisposition(string(disposition))
	if err != nil {
		return nil, nil, err
	}
	records, warnings, err := ForRunWithWarnings(runID)
	if err != nil {
		return nil, warnings, err
	}
	actions := make([]Action, 0, len(records))
	for _, rec := range records {
		action := Action{Record: rec, Path: rec.Path}
		if d == DispositionRetain {
			action.Action = ActionRetained
			actions = append(actions, action)
			continue
		}
		state, err := Inspect(rec)
		if err != nil {
			warning := fmt.Errorf("inspect %s: %w", rec.Path, err)
			warnings = append(warnings, warning)
			action.State = StateError
			action.Error = warning.Error()
			if d == DispositionRemoveUnchanged {
				action.Action = ActionSkipped
				actions = append(actions, action)
				continue
			}
		} else {
			action.State = state
		}
		if d == DispositionRemoveUnchanged && state == StateModified {
			action.Action = ActionSkipped
			actions = append(actions, action)
			continue
		}
		if err := Remove(rec, d == DispositionRemoveAll); err != nil {
			return actions, warnings, err
		}
		action.Action = ActionRemoved
		action.Removed = true
		actions = append(actions, action)
	}
	return actions, warnings, nil
}

func normalizeStatus(status []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(status), "\n"), "\r")
}

func repoHasWorktree(repo, path string) (bool, error) {
	out, err := gitOutput(context.Background(), repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("list source repository worktrees: %v: %s", err, strings.TrimSpace(string(out)))
	}
	path = filepath.Clean(path)
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		const prefix = "worktree "
		if bytes.HasPrefix(line, []byte(prefix)) && filepath.Clean(string(line[len(prefix):])) == path {
			return true, nil
		}
	}
	return false, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Cancel kills the whole group, so fetch helpers cannot hold the output pipe open.
	cmd := processguard.Command(ctx, "/usr/bin/git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return childenv.Clean(cmd).CombinedOutput()
}
