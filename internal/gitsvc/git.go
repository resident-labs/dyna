// Package gitsvc reads workspace git status and diffs.
package gitsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/childenv"
)

const gitTimeout = 8 * time.Second

const gitCommitFormat = "%H%x00%h%x00%s%x00%an%x00%aI"

// Service uses the caller's workspace resolver and existing preview byte limit.
type Service struct {
	WorkspaceRoot func(roomRef, sessionRef string) (string, error)
	MaxBytes      int
}

// CappedBuffer keeps draining the child after the limit to avoid broken
// pipes and unbounded allocation. CommandContext still bounds runtime.
type CappedBuffer struct {
	buf         bytes.Buffer
	Limit       int
	Truncated   bool
	stopAtLimit bool
}

func (b *CappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *CappedBuffer) String() string { return b.buf.String() }

func (b *CappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := b.Limit - b.buf.Len()
	if len(p) > left {
		p = p[:left]
		b.Truncated = true
	}
	_, _ = b.buf.Write(p)
	if b.stopAtLimit && b.Truncated {
		return len(p), errOutputLimit
	}
	return n, nil
}

var errOutputLimit = errors.New("git output limit reached")

func runWorkspaceGit(ctx context.Context, root string, limit int, args ...string) ([]byte, bool, error) {
	return runWorkspaceGitOutput(ctx, root, limit, false, args...)
}

func runWorkspaceGitOutput(ctx context.Context, root string, limit int, stopAtLimit bool, args ...string) ([]byte, bool, error) {
	// Diff headers must use the a/ and b/ prefixes the clients parse, whatever
	// the user's diff.mnemonicPrefix or diff.noprefix settings say.
	fixed := []string{"--no-pager", "--literal-pathspecs", "-c", "core.fsmonitor=", "-c", "color.ui=false",
		"-c", "diff.mnemonicPrefix=false", "-c", "diff.noprefix=false"}
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
	cmd.Dir = root
	// Inherited Git variables can silently point at another worktree or index.
	// User config remains available; external diff and textconv never run.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout := &CappedBuffer{Limit: limit, stopAtLimit: stopAtLimit}
	stderr := &CappedBuffer{Limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err := childenv.Clean(cmd).Run()
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("git timed out: %w", ctx.Err())
	}
	if err != nil && !(stopAtLimit && stdout.Truncated) {
		var exit *exec.ExitError
		isDifference := len(args) > 1 && args[0] == "diff" && args[1] == "--no-index" && errors.As(err, &exit) && exit.ExitCode() == 1
		if !isDifference {
			return nil, false, fmt.Errorf("git: %s: %w", strings.TrimSpace(stderr.String()), err)
		}
	}
	return stdout.Bytes(), stdout.Truncated, nil
}

// GitStatus reads NUL-delimited porcelain v2, so spaces, tabs and newlines
// in paths are preserved. A row may have both staged and unstaged changes.
func (s *Service) GitStatus(roomRef, sessionRef string) (GitStatusResult, error) {
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return GitStatusResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	b, truncated, err := runWorkspaceGit(ctx, root, s.MaxBytes, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return GitStatusResult{}, err
	}
	out := parseGitStatus(b, truncated)
	out.Root = root
	out.ChangedFiles = len(out.Files)
	// Porcelain paths are always relative to the repository top level.
	// A session can start in a subdirectory; keep every returned path usable
	// with the same ListEntries, ReadFile and GitDiff root.
	prefix, _, err := runWorkspaceGit(ctx, root, 4096, "rev-parse", "--show-prefix")
	if err != nil {
		return GitStatusResult{}, err
	}
	base := filepath.FromSlash(strings.TrimSuffix(string(prefix), "\n"))
	if base != "" {
		for i := range out.Files {
			f := &out.Files[i]
			f.Path, err = filepath.Rel(base, filepath.FromSlash(f.Path))
			if err != nil {
				return GitStatusResult{}, err
			}
			if f.OriginalPath != "" {
				f.OriginalPath, err = filepath.Rel(base, filepath.FromSlash(f.OriginalPath))
				if err != nil {
					return GitStatusResult{}, err
				}
			}
		}
	}
	dirs, _, err := runWorkspaceGit(ctx, root, 4096, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return GitStatusResult{}, err
	}
	if paths := strings.Split(strings.TrimSpace(string(dirs)), "\n"); len(paths) == 2 {
		out.Worktree = paths[0] != paths[1]
	}
	if out.OID != "" && out.OID != "(initial)" {
		log, cut, err := runWorkspaceGit(ctx, root, 128<<10, "log", "-5", "--no-show-signature", "--format="+gitCommitFormat+"%x00")
		if err != nil {
			return GitStatusResult{}, err
		}
		count, _, err := runWorkspaceGit(ctx, root, 128, "rev-list", "--count", "HEAD")
		if err != nil {
			return GitStatusResult{}, err
		}
		out.CommitCount, err = strconv.Atoi(strings.TrimSpace(string(count)))
		if err != nil {
			return GitStatusResult{}, err
		}
		out.Truncated = out.Truncated || cut
		fields := bytes.Split(log, []byte{0})
		for len(fields) >= 5 {
			out.Commits = append(out.Commits, parseCommit(fields))
			fields = fields[5:]
		}
	}
	return out, nil
}

func parseGitStatus(b []byte, truncated bool) GitStatusResult {
	out := GitStatusResult{Files: []GitFileStatus{}, Commits: []GitCommit{}, Truncated: truncated}
	records := bytes.Split(b, []byte{0})
	// The final chunk is incomplete when output was capped; never invent a path.
	if truncated {
		records = records[:len(records)-1]
	}
	for i := 0; i < len(records); i++ {
		r := string(records[i])
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "# ") {
			key, val, _ := strings.Cut(strings.TrimPrefix(r, "# "), " ")
			switch key {
			case "branch.oid":
				out.OID = val
			case "branch.head":
				out.Branch = val
			case "branch.upstream":
				out.Upstream = val
			case "branch.ab":
				fmt.Sscanf(val, "+%d -%d", &out.Ahead, &out.Behind)
			}
			continue
		}
		var f GitFileStatus
		switch r[0] {
		case '?':
			if !strings.HasPrefix(r, "? ") {
				continue
			}
			f = GitFileStatus{Path: r[2:], Status: "?", IndexStatus: "?", WorktreeStatus: "?", Untracked: true, Unstaged: true}
		case '1', '2', 'u':
			count := 9
			if r[0] == '2' {
				count = 10
			}
			if r[0] == 'u' {
				count = 11
			}
			fields := strings.SplitN(r, " ", count)
			if len(fields) != count || len(fields[1]) != 2 {
				continue
			}
			xy := fields[1]
			f = GitFileStatus{Path: fields[count-1], IndexStatus: xy[:1], WorktreeStatus: xy[1:], Staged: xy[0] != '.', Unstaged: xy[1] != '.', Conflicted: r[0] == 'u'}
			f.Status = f.WorktreeStatus
			if f.Status == "." {
				f.Status = f.IndexStatus
			}
			if f.Conflicted {
				f.Status = "U"
			}
			if r[0] == '2' {
				if i+1 >= len(records) || len(records[i+1]) == 0 {
					continue
				}
				i++
				f.OriginalPath = string(records[i])
				f.Status = xy[:1]
				if f.Status == "." {
					f.Status = xy[1:]
				}
			}
		default:
			continue
		}
		out.Files = append(out.Files, f)
	}
	return out
}

// GitDiff returns tracked changes against the index, or staged changes
// against HEAD. A path is a literal filename, never a Git pathspec pattern.
// Untracked files are previewed as additions with git diff --no-index.
func (s *Service) GitDiff(roomRef, sessionRef, path string, staged bool) (GitDiffResult, error) {
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return GitDiffResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	if path != "" {
		if filepath.IsAbs(path) {
			path, err = filepath.Rel(root, path)
			if err != nil {
				return GitDiffResult{}, err
			}
		}
		path = filepath.Clean(path)
		args = append(args, path)
	}
	b, truncated, err := runWorkspaceGit(ctx, root, s.MaxBytes, args...)
	if err != nil {
		return GitDiffResult{}, err
	}
	if len(b) == 0 && path != "" && !staged {
		// git diff omits untracked paths. Verify that this path is untracked
		// before generating an addition, so unchanged tracked files stay empty.
		untracked, _, e := runWorkspaceGit(ctx, root, s.MaxBytes, "ls-files", "--others", "--exclude-standard", "-z", "--", path)
		if e != nil {
			return GitDiffResult{}, e
		}
		if len(untracked) > 0 && bytes.Equal(bytes.TrimSuffix(untracked, []byte{0}), []byte(filepath.ToSlash(path))) {
			b, truncated, err = runWorkspaceGit(ctx, root, s.MaxBytes, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", os.DevNull, path)
			if err != nil {
				return GitDiffResult{}, err
			}
		}
	}
	text := strings.ToValidUTF8(string(b), "�")
	if len(text) > s.MaxBytes {
		text = strings.ToValidUTF8(text[:s.MaxBytes], "")
		truncated = true
	}
	return GitDiffResult{Text: text, Truncated: truncated}, nil
}

var prSubject = regexp.MustCompile(`(?:\(#([0-9]+)\)|^Merge pull request #([0-9]+)(?: |$))`)

func commitPR(subject string) int {
	match := prSubject.FindStringSubmatch(subject)
	if len(match) != 3 {
		return 0
	}
	number := match[1]
	if number == "" {
		number = match[2]
	}
	n, _ := strconv.Atoi(number)
	return n
}

// parseCommit decodes the five NUL-delimited fields in gitCommitFormat.
func parseCommit(fields [][]byte) GitCommit {
	date, _ := time.Parse(time.RFC3339, string(fields[4]))
	return GitCommit{Hash: strings.TrimLeft(string(fields[0]), "\n"), ShortHash: string(fields[1]), Subject: string(fields[2]), Author: string(fields[3]), Time: date, PRNumber: commitPR(string(fields[2]))}
}
