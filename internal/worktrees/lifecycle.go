package worktrees

import (
	"bytes"
	"context"
	"dyna-agent/internal/settings"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Change describes one commit, changed path, or omitted group of changes.
type Change struct {
	Kind                  string
	Commit, Subject       string
	Path, Action          string
	Insertions, Deletions string
	Omitted               int
}

// RepositoryRoot returns the repository containing dir.
func RepositoryRoot(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(string(out)), err
}

// Create creates a detached git worktree of repoDir at HEAD. The
// returned base commit is the snapshot the worker started from. Cleanup removes
// or retains it according to retention and reports whether it was retained.
func Create(ctx context.Context, repoDir, retention string) (string, string, func() bool, error) {
	return CreateForRun(ctx, repoDir, retention, os.Getenv("DYNA_RUN_ID"), os.Getenv("DYNA_SESSION"))
}

// CreateForRun records workflow ownership before the worker can start.
func CreateForRun(ctx context.Context, repoDir, retention, runID, session string) (string, string, func() bool, error) {
	if !settings.ValidWorktreesRetention(retention) {
		return "", "", nil, fmt.Errorf("worktree retention must be one of changed, all, none")
	}
	root, main, err := allocationRoot(ctx, repoDir)
	if err != nil {
		return "", "", nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", "", nil, err
	}
	wt, err := os.MkdirTemp(root, "workflow-")
	if err != nil {
		return "", "", nil, err
	}
	if out, err := gitOutput(ctx, repoDir, "worktree", "add", "--detach", wt, "HEAD"); err != nil {
		os.RemoveAll(wt)
		return "", "", nil, fmt.Errorf("worktree isolation requires a git repo at %s: %v: %s", repoDir, err, out)
	}
	baseCommit, err := gitOutput(ctx, wt, "rev-parse", "HEAD")
	if err != nil {
		gitOutput(context.Background(), repoDir, "worktree", "remove", "--force", wt)
		os.RemoveAll(wt)
		return "", "", nil, fmt.Errorf("resolve isolated worktree base at %s: %w", wt, err)
	}
	baseCommit = bytes.TrimSpace(baseCommit)
	rec := Record{Path: wt, Repo: main, Base: string(baseCommit), Head: string(baseCommit), Session: session, RunID: runID, CreatedAt: time.Now().UTC()}
	if err := Track(rec); err != nil {
		gitOutput(ctx, repoDir, "worktree", "remove", wt)
		return "", "", nil, err
	}
	cleanup := func() bool {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _, changed, err := Fingerprint(cleanupCtx, wt, string(baseCommit))
		if err != nil || retention == settings.WorktreesRetentionAll || (retention == settings.WorktreesRetentionChanged && changed) {
			return true
		}
		actions, err := GC(cleanupCtx, repoDir, GCOptions{Path: wt})
		if err != nil {
			return true
		}
		return len(actions) != 1 || actions[0].Action != "removed"
	}
	return wt, string(baseCommit), cleanup, nil
}

func Fingerprint(ctx context.Context, worktreeDir, baseCommit string) (head, status string, changed bool, err error) {
	headOut, err := gitOutput(ctx, worktreeDir, "rev-parse", "HEAD")
	if err != nil {
		return baseCommit, "", true, fmt.Errorf("resolve worktree HEAD: %v: %s", err, bytes.TrimSpace(headOut))
	}
	head = strings.TrimSpace(string(headOut))
	statusOut, err := gitOutput(ctx, worktreeDir, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return head, "", true, fmt.Errorf("resolve worktree status: %v: %s", err, bytes.TrimSpace(statusOut))
	}
	status = strings.TrimSuffix(strings.TrimSuffix(string(statusOut), "\n"), "\r")
	return head, status, head != strings.TrimSpace(baseCommit) || status != "", nil
}

func Changes(ctx context.Context, worktreeDir, baseCommit string, limit int) ([]Change, error) {
	commits, err := commitChanges(ctx, worktreeDir, baseCommit, limit)
	if err != nil {
		return nil, err
	}
	diffs, err := diffChanges(ctx, worktreeDir, baseCommit, limit)
	if err != nil {
		return nil, err
	}
	return append(commits, diffs...), nil
}

func commitChanges(ctx context.Context, worktreeDir, baseCommit string, limit int) ([]Change, error) {
	out, err := gitOutput(ctx, worktreeDir, "log", "--reverse", "--format=%h%x09%s", baseCommit+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("list commits since isolated base: %v: %s", err, bytes.TrimSpace(out))
	}
	text := strings.TrimSuffix(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	count := len(lines)
	if len(lines) > limit {
		lines = lines[:limit]
	}
	entries := make([]Change, 0, len(lines)+1)
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("parse commit since isolated base: unexpected git log record %q", line)
		}
		entries = append(entries, Change{
			Kind: "git-commit", Commit: parts[0], Subject: parts[1],
		})
	}
	if omitted := count - len(lines); omitted > 0 {
		entries = append(entries, Change{
			Kind: "git-commit", Omitted: omitted,
		})
	}
	return entries, nil
}

type worktreeLineCounts struct {
	insertions string
	deletions  string
}

func diffChanges(ctx context.Context, worktreeDir, baseCommit string, limit int) ([]Change, error) {
	tracked, err := gitOutput(ctx, worktreeDir, "diff", "--name-only", "-z", "--no-renames", baseCommit, "--")
	if err != nil {
		return nil, fmt.Errorf("list tracked changes since isolated base: %v: %s", err, bytes.TrimSpace(tracked))
	}
	untracked, err := gitOutput(ctx, worktreeDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list untracked changes since isolated base: %v: %s", err, bytes.TrimSpace(untracked))
	}
	numstat, err := gitOutput(ctx, worktreeDir, "diff", "--numstat", "-z", "--no-renames", baseCommit, "--")
	if err != nil {
		return nil, fmt.Errorf("count changed lines since isolated base: %v: %s", err, bytes.TrimSpace(numstat))
	}

	pathSet := make(map[string]struct{})
	for _, path := range append(splitNULRecords(tracked), splitNULRecords(untracked)...) {
		pathSet[path] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	counts := make(map[string]worktreeLineCounts)
	for _, record := range splitNULRecords(numstat) {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("parse changed line counts: unexpected git diff record %q", record)
		}
		if _, addErr := strconv.Atoi(parts[0]); addErr != nil {
			continue // binary files use "-" instead of line counts
		}
		if _, deleteErr := strconv.Atoi(parts[1]); deleteErr != nil {
			continue
		}
		counts[parts[2]] = worktreeLineCounts{insertions: parts[0], deletions: parts[1]}
	}

	total := len(paths)
	if len(paths) > limit {
		paths = paths[:limit]
	}
	entries := make([]Change, 0, len(paths)+1)
	for _, path := range paths {
		_, baseErr := gitOutput(ctx, worktreeDir, "cat-file", "-e", baseCommit+":"+path)
		_, currentErr := os.Lstat(filepath.Join(worktreeDir, filepath.FromSlash(path)))
		change := "modified"
		switch {
		case baseErr != nil:
			change = "created"
		case errors.Is(currentErr, os.ErrNotExist):
			change = "deleted"
		}
		record := Change{Kind: "git-diff", Action: change, Path: path}
		if lineCounts, ok := counts[path]; ok {
			record.Insertions, record.Deletions = lineCounts.insertions, lineCounts.deletions
		}
		entries = append(entries, record)
	}
	if omitted := total - len(paths); omitted > 0 {
		entries = append(entries, Change{
			Kind: "git-diff", Omitted: omitted,
		})
	}
	return entries, nil
}

func splitNULRecords(data []byte) []string {
	records := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(records))
	for _, record := range records {
		if len(record) != 0 {
			out = append(out, string(record))
		}
	}
	return out
}
