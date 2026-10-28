package worktrees

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"dyna-agent/internal/locallane"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
)

// Tree describes a repository worktree and its last cleanup decision.
type Tree struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Owner       string `json:"owner"`
	OwnerSource string `json:"ownerSource,omitempty"`
	inferred    inferredOwners
	Merged      bool          `json:"merged"`
	Ahead       int           `json:"ahead"`
	DirtyFiles  []string      `json:"dirtyFiles,omitempty"`
	Dirty       bool          `json:"dirty"`
	Size        int64         `json:"size"`
	Age         time.Duration `json:"age"`
	Reason      string        `json:"reason"`
	// Main marks the repository's main checkout, which cleanup never removes.
	Main bool `json:"main,omitempty"`
	// OwnerOpen marks a clean, merged tree kept only because its owner is
	// still open or running. That is expected and needs nobody's action.
	OwnerOpen bool `json:"ownerOpen,omitempty"`
	// Deferred marks a tree that cleanup ran out of time to finish. The
	// scheduled maintenance pass retries it; nobody needs to act.
	Deferred  bool   `json:"deferred,omitempty"`
	Action    string `json:"action"`
	sessionID string
	runID     string
	head      string
	protected string
	touched   time.Time
}

type GCOptions struct {
	DryRun      bool
	Session     string
	Branch      string
	OwnerClosed func(string) (bool, error)
	// Path limits workflow cleanup to the worktree it created.
	Path string
	// history carries what ended workflow runs' journals said from the first
	// scan into removal rechecks. Reading every journal again for each tree
	// spent session cleanup's whole time budget. Sessions and running
	// workflows are still read again before each removal.
	history *runHistory
	// beforeRemove lets tests observe each removal worker; nil in production.
	beforeRemove func(path string)
}

// ManagedRoot is shared by session and workflow worktrees.
func ManagedRoot() (string, error) {
	v, err := settings.Load("")
	if err != nil {
		return "", err
	}
	root := v.WorktreesRoot
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".dyna-worktrees")
	}
	return filepath.Abs(root)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func managedPath(path string) bool {
	root, err := ManagedRoot()
	if err != nil || !within(root, path) {
		return false
	}
	// Resolve existing ancestors to prevent a symlink from escaping the root.
	resolvedRoot, err := resolveAncestors(root)
	if err != nil {
		return false
	}
	resolved, err := resolveAncestors(path)
	return err == nil && within(resolvedRoot, resolved)
}

// validateRecordPath also accepts the root captured when the record was made.
// Changing worktrees.root must not strand trees in the previous root.
func validateRecordPath(rec Record) (string, error) {
	if safe, err := validatePath(rec.Path); err == nil {
		return safe, nil
	}
	path, err := filepath.Abs(rec.Path)
	if err != nil {
		return "", err
	}
	if rec.Root == "" || !filepath.IsAbs(rec.Root) || rec.Repo == "" {
		return "", fmt.Errorf("unsafe worktree record path %q", rec.Path)
	}
	root, err := resolveAncestors(rec.Root)
	if err != nil {
		return "", err
	}
	resolved, err := resolveAncestors(path)
	if err != nil {
		return "", err
	}
	repo, err := resolveAncestors(rec.Repo)
	if err != nil {
		return "", err
	}
	if root == repo || within(repo, root) || !within(root, resolved) {
		return "", fmt.Errorf("unsafe worktree record root %q", rec.Root)
	}
	return filepath.Clean(path), nil
}

func resolveAncestors(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolveAncestors(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}

func commonRepo(ctx context.Context, repo string) (string, error) {
	out, err := gitOutput(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git repository: %w: %s", err, out)
	}
	common := strings.TrimSpace(string(out))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return common, nil
}

func allocationRoot(ctx context.Context, repo string) (string, string, error) {
	main, err := commonRepo(ctx, repo)
	if err != nil {
		return "", "", err
	}
	root, err := ManagedRoot()
	if err != nil {
		return "", "", err
	}
	canonicalMain, err := resolveAncestors(main)
	if err != nil {
		return "", "", err
	}
	canonicalRoot, err := resolveAncestors(root)
	if err != nil {
		return "", "", err
	}
	if canonicalRoot == canonicalMain || within(canonicalMain, canonicalRoot) {
		return "", "", fmt.Errorf("worktree root must be outside the main checkout %s", main)
	}
	sum := sha256.Sum256([]byte(canonicalMain))
	return filepath.Join(root, fmt.Sprintf("%s-%x", filepath.Base(main), sum[:4])), main, nil
}

func slug(branch string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, branch)
}

func defaultBranch(ctx context.Context, repo string) (repoid.Branch, error) {
	return repoid.DefaultBranch(ctx, repo, func(ctx context.Context, dir string, args ...string) (string, error) {
		out, err := gitOutput(ctx, dir, args...)
		return strings.TrimSpace(string(out)), err
	})
}

// NewFor creates a worktree with the caller identity supplied by the engine.
// HasCommit reports whether the repository has a commit to branch from. A
// fresh `git init` has an unborn HEAD, so it has no main to copy yet.
func HasCommit(ctx context.Context, repo string) bool {
	out, err := gitOutput(ctx, repo, "rev-parse", "--verify", "HEAD")
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func NewFor(ctx context.Context, repo, branch, base, session, runID string) (Record, error) {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return Record{}, errors.New("branch is required and must not start with '-'")
	}
	if base == "" {
		enabled, err := locallane.Enabled(ctx, repo)
		if err != nil {
			return Record{}, err
		}
		if enabled {
			if _, err := locallane.Ensure(ctx, repo); err != nil {
				return Record{}, err
			}
			base = locallane.Integration
		} else {
			resolved, err := defaultBranch(ctx, repo)
			if err != nil {
				return Record{}, err
			}
			base = resolved.Ref
		}
	}
	if strings.HasPrefix(base, "-") {
		return Record{}, errors.New("invalid base")
	}
	if _, err := gitOutput(ctx, repo, "check-ref-format", "--branch", branch); err != nil {
		return Record{}, fmt.Errorf("invalid branch: %w", err)
	}
	root, main, err := allocationRoot(ctx, repo)
	if err != nil {
		return Record{}, err
	}
	// Pin the base to a commit before the worktree is created: the refresh
	// fetch rewrites FETCH_HEAD (D-164), and the new branch must start at
	// exactly the commit the caller's repository resolves the base to.
	pinned, err := pinBase(ctx, repo, main, base)
	if err != nil {
		return Record{}, err
	} // A hash keeps branch names such as a/b and a-b distinct.
	sum := sha256.Sum256([]byte(branch))
	name := slug(branch)
	if len(name) > 80 {
		name = name[:80]
	}
	path := filepath.Join(root, fmt.Sprintf("%s-%x", name, sum[:4]))
	if err := os.MkdirAll(root, 0700); err != nil {
		return Record{}, err
	}
	// The base ref, not the pinned id, is the start point: git derives the new
	// branch's upstream from it exactly as before (branch.autoSetupMerge), and
	// a raw commit id would drop that tracking. The add runs in the caller's
	// checkout, because HEAD and FETCH_HEAD are per-worktree refs that the
	// main checkout can resolve differently or not at all.
	if out, err := gitOutput(ctx, repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return Record{}, fmt.Errorf("create worktree: %w: %s", err, out)
	}
	// The pin and the add are separate git runs, so a concurrent fetch can
	// still move the base between them. Refuse loudly instead of starting the
	// branch at a commit other than the pinned one.
	if out, err := gitOutput(ctx, path, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(string(out)) != pinned {
		gitOutput(ctx, main, "worktree", "remove", "--force", path)
		// No user work exists on the new branch yet; remove it, so a retry
		// with the same name starts cleanly.
		gitOutput(ctx, main, "branch", "-D", branch)
		if err != nil {
			return Record{}, fmt.Errorf("verify the new worktree's commit: %w: %s", err, out)
		}
		return Record{}, fmt.Errorf("base %q moved from %s while the worktree was created; retry with the exact commit", base, pinned)
	}
	head, status, _, err := Fingerprint(ctx, path, pinned)
	if err != nil {
		gitOutput(ctx, main, "worktree", "remove", "--force", path)
		return Record{}, fmt.Errorf("fingerprint the new worktree: %w", err)
	}
	rec := Record{Path: path, Repo: main, Branch: branch, Base: base, Head: head, Status: status, Session: session, RunID: runID, CreatedAt: time.Now().UTC()}
	if err := Track(rec); err != nil {
		// Delete only the branch we created, and only if Git removed the clean
		// tree and the branch still points at the pinned commit.
		if out, removeErr := gitOutput(ctx, main, "worktree", "remove", path); removeErr != nil {
			return Record{}, errors.Join(err, fmt.Errorf("roll back worktree: %w: %s", removeErr, out))
		}
		if out, deleteErr := gitOutput(ctx, main, "update-ref", "-d", "refs/heads/"+branch, pinned); deleteErr != nil {
			return Record{}, errors.Join(err, fmt.Errorf("roll back branch: %w: %s", deleteErr, out))
		}
		return Record{}, err
	}
	// Track assigns the port block, so return the stored record.
	if stored, err := Get(path); err == nil {
		rec = stored
	}
	return rec, nil
}

// SessionClosed reads lifecycle metadata without recovery. Visible done
// sessions keep their worktrees until the owner dismisses them.
func SessionClosed(id string) (bool, error) {
	if filepath.Base(id) != id || id == "." || id == "" {
		return false, errors.New("invalid session id")
	}
	m, err := sessionlife.Read(runstore.SessionsDir(), id)
	if err != nil {
		return false, err
	}
	return m.IsClosed(), nil
}

func workflowClosed(id string) (bool, error) {
	if filepath.Base(id) != id || id == "." || id == "" {
		return false, errors.New("invalid workflow id")
	}
	meta, err := runstore.ReadMeta(id)
	if os.IsNotExist(err) {
		// A run folder with no row (a broken or starting run) still owns
		// its trees: it counts as open, never as unknown.
		if info, statErr := os.Stat(runstore.RunDir(id)); statErr == nil && info.IsDir() {
			return false, nil
		}
	}
	if err != nil {
		return false, err
	}
	return meta.Status != "running" && !meta.EndedAt.IsZero(), nil
}

// Scan includes hand-created worktrees, regardless of their directory.
func Scan(ctx context.Context, repo string) ([]Tree, error) {
	return scan(ctx, repo, nil)
}

func worktreeBlocks(ctx context.Context, repo string) ([]string, error) {
	out, err := gitOutput(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w: %s", err, out)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00\x00"), "\x00\x00"), nil
}

func scan(ctx context.Context, repo string, opts *GCOptions) ([]Tree, error) {
	blocks, err := worktreeBlocks(ctx, repo)
	if err != nil {
		return nil, err
	}

	return scanBlocks(ctx, repo, opts, blocks)
}

func scanBlocks(ctx context.Context, repo string, opts *GCOptions, blocks []string) ([]Tree, error) {
	records, warnings, err := ListWithWarnings()
	if err != nil {
		return nil, err
	}
	// A damaged owner record must not turn an owned tree into an unknown tree.
	if len(warnings) > 0 {
		return nil, fmt.Errorf("cannot trust worktree ownership: %w", errors.Join(warnings...))
	}
	owners := map[string]Record{}
	for _, r := range records {
		owners[filepath.Clean(r.Path)] = r
	}
	var paths []string
	for _, field := range strings.Split(strings.Join(blocks, "\x00"), "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			paths = append(paths, strings.TrimPrefix(field, "worktree "))
		}
	}
	if opts != nil && opts.Session != "" {
		paths, err = sessionWorktreePaths(ctx, paths, owners, opts.Session)
		if err != nil {
			return nil, fmt.Errorf("cannot select session worktrees safely: %w", err)
		}
		if len(paths) == 0 {
			return nil, nil
		}
	}
	// Infer owners only for the trees this scan reports. A removal recheck
	// names one tree; inferring all of them is most of a cleanup's time.
	if opts != nil && opts.Path != "" {
		paths = slices.DeleteFunc(paths, func(p string) bool { return filepath.Clean(p) != filepath.Clean(opts.Path) })
	}
	var history *runHistory
	if opts != nil {
		history = opts.history
	}
	inferred, inferenceErr := inferOwners(ctx, paths, history)
	if inferenceErr != nil {
		return nil, fmt.Errorf("cannot infer worktree owners safely: %w", inferenceErr)
	}
	defaultRef := ""
	if resolved, err := defaultBranch(ctx, repo); err == nil {
		prefix := "refs/heads/"
		if hasRemote(ctx, repo, "origin") {
			prefix = "refs/remotes/origin/"
		}
		defaultRef = prefix + resolved.Name
	}
	var trees []Tree
	for index, block := range blocks {
		t := Tree{Ahead: -1}
		for _, field := range strings.Split(block, "\x00") {
			key, value, _ := strings.Cut(field, " ")
			switch key {
			case "worktree":
				t.Path = value
			case "HEAD":
				t.head = value
			case "branch":
				t.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "locked":
				t.protected = "worktree is locked"
			case "bare":
				t.protected = "bare repository"
			case "prunable":
				t.protected = "worktree is missing or prunable"
			}
		}
		if t.Path == "" {
			continue
		}
		if index == 0 {
			t.protected = "main checkout"
			t.Main = true
		}
		if rec, ok := owners[filepath.Clean(t.Path)]; ok {
			t.Owner = rec.Session
			t.sessionID = rec.Session
			t.runID = rec.RunID
			if t.Owner == "" && t.runID != "" {
				t.Owner = "run:" + t.runID
			}
		}
		var selected bool
		t.inferred, selected = inferred[t.Path]
		if !selected {
			continue
		}
		var sources []string
		for _, owner := range t.inferred.Sessions {
			sources = append(sources, "owner inferred from session "+owner.Address)
		}
		for _, owner := range t.inferred.Runs {
			sources = append(sources, "owner inferred from workflow "+owner.ID)
		}
		if len(sources) > 5 {
			sources = append(sources[:5], fmt.Sprintf("and %d other lifecycle references", len(sources)-5))
		}
		t.OwnerSource = strings.Join(sources, "; ")
		if t.Owner == "" {
			var names []string
			for _, o := range t.inferred.Sessions {
				names = append(names, o.Address)
			}
			for _, o := range t.inferred.Runs {
				names = append(names, "run:"+o.ID)
			}
			if len(names) > 5 {
				names = append(names[:5], fmt.Sprintf("and %d others", len(names)-5))
			}
			t.Owner = strings.Join(names, ", ")
		}
		if opts != nil && ((opts.Session != "" && !ownedBy(t, opts.Session)) || (opts.Path != "" && t.Path != opts.Path) || (opts.Branch != "" && t.Branch != opts.Branch)) {
			continue
		}
		trees = append(trees, t)
	}
	// Each tree needs several git calls and a file walk. They are independent,
	// so run a bounded number at once; one slow tree must not serialize the rest.
	var wg sync.WaitGroup
	slots := make(chan struct{}, scanParallelism)
	for i := range trees {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			inspectTree(ctx, repo, defaultRef, opts, &trees[i])
		}()
	}
	wg.Wait()
	return trees, nil
}

// clone copies the cached history so a worker can extend it without a lock.
func (h *runHistory) clone() *runHistory {
	if h == nil {
		return nil
	}
	c := &runHistory{read: maps.Clone(h.read), refs: make(map[string][]ownerRef, len(h.refs))}
	for k, v := range h.refs {
		c.refs[k] = slices.Clone(v)
	}
	return c
}

// scanParallelism bounds concurrent per-tree git and file inspection.
const scanParallelism = 8

// inspectTree fills in the git state and file activity of one tree.
func inspectTree(ctx context.Context, repo, defaultRef string, opts *GCOptions, tp *Tree) {
	t := *tp
	defer func() { *tp = t }()
	if info, err := os.Lstat(t.Path); err != nil {
		t.protected = "worktree is missing or unreadable"
	} else {
		// The directory's own mtime is a lower bound on the newest file
		// activity. Recent unknown trees cannot be removal candidates yet.
		t.touched = info.ModTime()
		t.Age = max(time.Since(t.touched), 0)
		_, status, _, e := Fingerprint(ctx, t.Path, t.head)
		t.Dirty = status != ""
		if status != "" {
			t.DirtyFiles = strings.Split(status, "\n")
		}
		t.Ahead = -1
		if defaultRef != "" {
			if count, err := gitOutput(ctx, repo, "rev-list", "--count", t.head, "--not", defaultRef); err == nil {
				t.Ahead, _ = strconv.Atoi(strings.TrimSpace(string(count)))
			}
		}
		if e != nil {
			t.protected = "cannot inspect git status: " + e.Error()
			t.Dirty = true
		}
		if defaultRef != "" {
			_, e = gitOutput(ctx, repo, "merge-base", "--is-ancestor", t.head, defaultRef)
			t.Merged = e == nil
		}
		if usageNeeded(t, opts) {
			size, touched, walkErr := treeUsage(ctx, t.Path)
			t.Size, t.touched = size, touched
			if walkErr != nil {
				t.protected = "cannot inspect all worktree files: " + walkErr.Error()
			}
			// Git metadata lives outside the worktree; it records checkout and commit activity.
			gitDir, gitErr := gitOutput(ctx, t.Path, "rev-parse", "--absolute-git-dir")
			if gitErr != nil {
				t.protected = "cannot inspect git metadata"
			} else {
				_, mt, e := treeUsage(ctx, strings.TrimSpace(string(gitDir)))
				if e != nil {
					t.protected = "cannot inspect git metadata"
				}
				if mt.After(t.touched) {
					t.touched = mt
				}
			}
			t.Age = time.Since(t.touched)
			if t.Age < 0 {
				t.Age = 0
			}
		}
	}
}

// usageNeeded asks the same retention rule whether file activity can matter.
// The top directory already proves a recently touched unknown tree must
// stay. Older candidates need the full walk to find recent nested files.
// Explicit inventory still reports the complete size and activity age.
func usageNeeded(t Tree, opts *GCOptions) bool {
	if opts == nil {
		return true
	}
	closed := opts.OwnerClosed
	if closed == nil {
		closed = SessionClosed
	}
	return decide(t, closed).Action == "would remove"
}

func treeUsage(ctx context.Context, path string) (int64, time.Time, error) {
	var size int64
	var latest time.Time
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return size, latest, err
}

// remove rechecks ownership, file activity, status and ancestry, then removes
// the tree or returns it kept with the reason. Sessions and running workflows
// are read again; only ended runs' journal history comes from the first scan.
func remove(ctx context.Context, repo string, t Tree, history *runHistory, closed func(string) (bool, error)) (out Tree) {
	// Whichever check stops it, a tree kept only because time ran out is
	// retried by the scheduled cleanup.
	defer func() {
		if out.Action == "kept" && ctx.Err() != nil {
			out.Deferred = true
			out.Reason = "cleanup ran out of time; the scheduled cleanup retries it"
		}
	}()
	latest, e := scan(ctx, repo, &GCOptions{Path: t.Path, history: history})
	if e != nil || len(latest) != 1 {
		t.Action, t.Reason = "kept", "cannot recheck worktree before removal"
		return t
	}
	if fresh := decide(latest[0], closed); fresh.Action != "would remove" || fresh.head != t.head {
		t.Action, t.Reason = "kept", "worktree changed during cleanup: "+fresh.Reason
		return t
	}
	// Git also checks for changes when removing the directory.
	_, status, _, e := Fingerprint(ctx, t.Path, t.head)
	head, e2 := gitOutput(ctx, t.Path, "rev-parse", "HEAD")
	if e != nil || status != "" || e2 != nil || strings.TrimSpace(string(head)) != t.head {
		t.Action, t.Reason = "kept", "worktree changed during cleanup"
		return t
	}
	if reason, e := processUse(ctx, t.Path); e != nil {
		t.Action, t.Reason = "kept", "cannot check worktree process use: "+e.Error()
		return t
	} else if reason != "" {
		t.Action, t.Reason = "kept", reason
		return t
	}
	// Rechecks above run in parallel; Git changes the shared worktree list one at a time.
	removeMu.Lock()
	defer removeMu.Unlock()
	if out, e := gitOutput(ctx, repo, "worktree", "remove", t.Path); e != nil {
		t.Action, t.Reason = "kept", fmt.Sprintf("git refused removal: %v: %s", e, strings.TrimSpace(string(out)))
		return t
	}
	t.Action = "removed"
	if e := MarkRemoved(t.Path); e != nil && managedPath(t.Path) {
		t.Reason += "; registry: " + e.Error()
	}
	return t
}

// removeMu serializes the final Git removal and registry update across workers.
var removeMu sync.Mutex

// pruneTimeout bounds git worktree prune on its own context.
const pruneTimeout = 10 * time.Second

func refreshDefaultBranch(ctx context.Context, repo string) (string, error) {
	resolved, err := defaultBranch(ctx, repo)
	if err != nil {
		return "cannot resolve repository default branch", err
	}
	if !hasRemote(ctx, repo, "origin") {
		// A local-only repository already has the default branch it compares
		// against. There is nothing to fetch and no remote error to report.
		return "", nil
	}
	remoteRef := "refs/heads/" + resolved.Name
	out, err := gitOriginOutput(ctx, repo, "fetch", "--no-tags", "origin", "+"+remoteRef+":refs/remotes/origin/"+resolved.Name)
	if err == nil {
		return "", nil
	}
	reason := "cannot verify remote " + resolved.Name + ": " + strings.TrimSpace(string(out))
	fetchErr := fmt.Errorf("fetch origin before cleanup: %w: %s", err, out)
	// An outside clone can lack the selected branch. Keep its trees without
	// reporting a cleanup failure; an unreachable remote remains an error.
	var missing *exec.ExitError
	if _, lsErr := gitOriginOutput(ctx, repo, "ls-remote", "--exit-code", "origin", remoteRef); errors.As(lsErr, &missing) && missing.ExitCode() == 2 {
		return "remote has no " + resolved.Name + " branch", nil
	}
	return reason, fetchErr
}

// GC applies the same conservative rule to every repository worktree.
// It never removes a branch and never forces Git to remove a directory.
func GC(ctx context.Context, repo string, opts GCOptions) ([]Tree, error) {
	// Cleanup may remove the linked worktree supplied by its caller. Run all
	// Git commands from the common repository so later rechecks and prune survive.
	main, err := commonRepo(ctx, repo)
	if err != nil {
		return nil, err
	}
	repo = main
	blocks, err := worktreeBlocks(ctx, repo)
	if err != nil {
		return nil, err
	}
	// The first entry is always protected. With no linked worktrees, no
	// removal depends on remote ancestry, including in local-only repositories.
	if len(blocks) > 1 {
		if reason, fetchErr := refreshDefaultBranch(ctx, repo); reason != "" || fetchErr != nil {
			trees, scanErr := scanBlocks(ctx, repo, &opts, blocks)
			for i := range trees {
				trees[i].Action = "kept"
				trees[i].Reason = reason
			}
			return trees, errors.Join(fetchErr, scanErr)
		}
	}

	opts.history = &runHistory{}
	trees, err := scanBlocks(ctx, repo, &opts, blocks)
	if err != nil {
		return nil, err
	}
	closed := opts.OwnerClosed
	if closed == nil {
		closed = SessionClosed
	}
	result := make([]Tree, 0, len(trees))
	var pending []int
	for _, t := range trees {
		if opts.Session != "" && !ownedBy(t, opts.Session) {
			continue
		}
		if opts.Path != "" && t.Path != opts.Path {
			continue
		}
		t = decide(t, closed)
		if t.Action == "would remove" && !opts.DryRun {
			pending = append(pending, len(result))
		}
		result = append(result, t)
	}
	// Each removal rereads ownership, status and process use right before it
	// removes its own tree, so rechecks of different trees can overlap. Workers
	// keep their own copy of the run history because it is not safe to share.
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(scanParallelism, len(pending)) {
		wg.Add(1)
		history := opts.history.clone()
		go func() {
			defer wg.Done()
			for i := range jobs {
				if opts.beforeRemove != nil {
					opts.beforeRemove(result[i].Path)
				}
				result[i] = remove(ctx, repo, result[i], history, closed)
			}
		}()
	}
	for _, i := range pending {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if !opts.DryRun {
		// Prune only drops metadata of missing trees, takes milliseconds, and
		// runs again on the next cleanup. A spent cleanup budget must not fail it.
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pruneTimeout)
		defer cancel()
		if out, e := gitOutput(pctx, repo, "worktree", "prune"); e != nil {
			return result, fmt.Errorf("prune worktree metadata: %w: %s", e, out)
		}
	}
	return result, nil
}

// processUse checks live working directories immediately before removal.
// Linux reports physical paths in /proc; resolve worktree symlinks to match.
func processUse(ctx context.Context, path string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", nil
	}
	tree, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		pid := entry.Name()
		if _, err := strconv.Atoi(pid); err != nil || !entry.IsDir() {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd"))
		// Processes can exit during the scan. Other users' entries may be unreadable.
		if err != nil || (cwd != tree && !within(tree, cwd)) {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
		if err != nil {
			comm = []byte("unknown")
		}
		return fmt.Sprintf("in use by process %s (%s)", pid, strings.TrimSpace(string(comm))), nil
	}
	return "", ctx.Err()
}

// ReasonDirty and ReasonNotMerged are the stable reasons cleanup gives a tree
// it keeps. The worktree event reporter maps them to its reason class, so a
// reword stays in step with the event.
const (
	ReasonDirty     = "uncommitted changes or untracked files"
	ReasonNotMerged = "HEAD is not merged into the repository default branch"
)

func decide(t Tree, closed func(string) (bool, error)) Tree {
	t.Action = "kept"
	switch {
	case t.protected != "":
		t.Reason = t.protected
	case t.sessionID == "unverified":
		t.Reason, t.OwnerOpen = "owning session is unverified", true
	case t.Dirty:
		t.Reason = ReasonDirty
	case !t.Merged:
		t.Reason = ReasonNotMerged
	default:
		known := t.sessionID != "" || t.runID != ""
		isClosed := false
		if t.runID != "" {
			runClosed, e := workflowClosed(t.runID)
			if e == nil && !runClosed {
				t.Reason, t.OwnerOpen = "owning workflow is running", true
			} else if e != nil && !os.IsNotExist(e) {
				t.Reason = "cannot read owning workflow: " + e.Error()
			}
			if t.Owner == "run:"+t.runID {
				known = e == nil
				isClosed = runClosed
			}
		}
		if t.sessionID != "" {
			var e error
			isClosed, e = closed(t.sessionID)
			if os.IsNotExist(e) {
				known = false
			} else if e != nil {
				t.Reason, t.OwnerOpen = "cannot read owning session: "+e.Error(), false
			}
		}
		for _, owner := range append(append([]ownerRef{}, t.inferred.Sessions...), t.inferred.Runs...) {
			if !owner.Closed {
				t.Reason, t.OwnerOpen = "an inferred owner is still open", true
			} else if !known {
				known = true
				isClosed = true
			}
		}
		if t.Reason == "" {
			switch {
			case known && !isClosed:
				t.Reason, t.OwnerOpen = "owning session is open", true
			case !known && t.Age < 24*time.Hour:
				t.Reason = "unknown owner; untouched for less than 24 hours"
			default:
				t.Action = "would remove"
				t.Reason = "clean, merged, and owner closed or unknown for at least 24 hours"
			}
		}
	}

	if t.OwnerSource != "" {
		t.Reason += "; " + t.OwnerSource
	}
	return t
}

func ownedBy(t Tree, id string) bool {
	if t.sessionID == id {
		return true
	}
	for _, o := range t.inferred.Sessions {
		if o.ID == id {
			return true
		}
	}
	return false
}
