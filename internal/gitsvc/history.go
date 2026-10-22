package gitsvc

import (
	"bytes"
	"context"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/harness/runtime"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxHistoryPage = 200
const maxBlameLines = 20000

type GitHistoryCommit struct {
	GitCommit
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Moved   bool   `json:"moved"`
	Path    string `json:"path,omitempty"`
	oldPath string
}

type GitHistoryResult struct {
	Commits   []GitHistoryCommit `json:"commits"`
	Earlier   bool               `json:"earlier"`
	Cursor    string             `json:"cursor"`
	Truncated bool               `json:"truncated"`
}

type historyCursor struct {
	Head     string `json:"head"`
	Offset   int    `json:"offset"`
	Path     string `json:"path"`
	WalkPath string `json:"walkPath"`
	Parent   bool   `json:"parent"`
}

var objectHash = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// GitHistory returns newest commits first, with an exclusive opaque cursor.
// Repository cursors pin HEAD and preserve graph order across pages.
// File pages end at a rename and continue from its parent with the old name.
func (s *Service) GitHistory(roomRef, sessionRef, path, before string, limit int) (GitHistoryResult, error) {
	out := GitHistoryResult{Commits: []GitHistoryCommit{}}
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return out, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > maxHistoryPage {
		limit = maxHistoryPage
	}
	if filepath.IsAbs(path) {
		path, err = filepath.Rel(root, path)
		if err != nil {
			return out, err
		}
	}
	cursor := historyCursor{Path: path, WalkPath: path}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	prefix := ""
	if path != "" {
		prefix, err = workspaceGitPrefix(ctx, root)
		if err != nil {
			return out, err
		}
	}

	if before != "" {
		if len(before) > 5500 {
			return out, errors.New("invalid history cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(before)
		if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &cursor) != nil || !objectHash.MatchString(cursor.Head) || cursor.Offset < 0 || (cursor.Offset == 0 && !cursor.Parent) || cursor.Offset > 1000000 || cursor.Path != path {
			return out, errors.New("invalid history cursor")
		}
	} else {
		head, cut, err := runWorkspaceGit(ctx, root, 128, "rev-parse", "--revs-only", "HEAD")
		if err != nil {
			return out, err
		}
		if cut {
			return out, errors.New("git HEAD exceeded output limit")
		}
		cursor.Head = strings.TrimSpace(string(head))
		if cursor.Head == "" {
			return out, nil
		}
	}
	args := []string{"log", "--no-show-signature", "--no-ext-diff", "--no-textconv", "--format=%x00" + gitCommitFormat + "%x00", "--numstat", "--topo-order", "--diff-merges=first-parent", "-z", "--find-renames", fmt.Sprintf("--max-count=%d", limit+1), fmt.Sprintf("--skip=%d", cursor.Offset)}
	if path != "" {
		args = append(args, "--follow")
	}
	revision := cursor.Head
	if path != "" && cursor.Parent {
		revision += "^"
	}
	args = append(args, revision, "--")
	if path != "" {
		args = append(args, cursor.WalkPath)
	}
	data, cut, err := runWorkspaceGit(ctx, root, s.MaxBytes, args...)
	if err != nil {
		return out, err
	}

	commits := parseHistory(data, cut)
	out.Truncated = cut
	out.Earlier = len(commits) > limit || (cut && len(commits) > 0)
	if len(commits) > limit {
		commits = commits[:limit]
	}
	// Git does not follow renames in commits omitted with --skip. End a file
	// page at a rename and continue from its parent using the previous name.
	renamed := false
	if path != "" {
		for i, commit := range commits {
			if commit.oldPath != "" {
				out.Earlier = true
				commits = commits[:i+1]
				cursor.Head = commit.Hash
				cursor.Parent = true
				cursor.WalkPath, err = filepath.Rel(filepath.FromSlash(prefix), filepath.FromSlash(commit.oldPath))
				if err != nil {
					return out, err
				}
				cursor.Offset = 0
				renamed = true
				break
			}
		}
	}
	if path != "" {
		for i := range commits {
			if commits[i].Path != "" {
				commits[i].Path, err = filepath.Rel(filepath.FromSlash(prefix), filepath.FromSlash(commits[i].Path))
				if err != nil {
					return out, err
				}
			}
		}
	} else {
		for i := range commits {
			commits[i].Path = ""
		}
	}
	out.Commits = commits
	if out.Earlier && len(commits) > 0 {
		if !renamed {
			cursor.Offset += len(commits)
		}
		raw, _ := json.Marshal(cursor)
		out.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}

	return out, nil
}

func parseHistory(data []byte, cut bool) []GitHistoryCommit {
	out := []GitHistoryCommit{}
	fields := bytes.Split(data, []byte{0})
	// A capped last commit can have incomplete statistics. Return only complete
	// commits so its next request can read it with a fresh output budget.
	var current *GitHistoryCommit
	for i := 0; i < len(fields); i++ {
		field := string(fields[i])
		if field == "" && i+5 < len(fields) && objectHash.Match(fields[i+1]) {
			if current != nil {
				out = append(out, *current)
			}
			current = &GitHistoryCommit{GitCommit: parseCommit(fields[i+1 : i+6])}
			i += 5
			continue
		}
		if current == nil {
			continue
		}
		stat := strings.SplitN(strings.TrimPrefix(field, "\n"), "\t", 3)
		if len(stat) != 3 {
			continue
		}
		added, _ := strconv.Atoi(stat[0])
		deleted, _ := strconv.Atoi(stat[1])
		current.Added += added
		current.Deleted += deleted
		current.Path = stat[2]
		if stat[2] == "" {
			current.Moved = true
			if i+2 < len(fields) {
				current.oldPath = string(fields[i+1])
				current.Path = string(fields[i+2])
			}
			i += 2
		}
	}
	if !cut && current != nil {
		out = append(out, *current)
	}
	return out
}

type GitBlameRange struct {
	StartLine int       `json:"startLine"`
	EndLine   int       `json:"endLine"`
	Commit    GitCommit `json:"commit"`
}

type GitBlameResult struct {
	Ranges    []GitBlameRange `json:"ranges"`
	Truncated bool            `json:"truncated"`
}

// GitBlame runs only for the explicitly requested file. Output bytes and line
// count are bounded; no blame work runs when listing files or history.
func (s *Service) GitBlame(roomRef, sessionRef, path string) (GitBlameResult, error) {
	out := GitBlameResult{Ranges: []GitBlameRange{}}
	if path == "" {
		return out, errors.New("a file path is required")
	}
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return out, err
	}
	if filepath.IsAbs(path) {
		path, err = filepath.Rel(root, path)
		if err != nil {
			return out, err
		}
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("blame requires a regular file")
	}
	if info.Size() > int64(s.MaxBytes) {
		return out, errors.New("file exceeds blame size limit")
	}
	if info.Size() == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	data, cut, err := runWorkspaceGit(ctx, root, s.MaxBytes, "blame", "--no-textconv", "--line-porcelain", "-L", fmt.Sprintf("1,%d", maxBlameLines+1), "--", path)
	if err != nil {
		return out, err
	}
	out.Truncated = cut
	var commit GitCommit
	line := 0
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		text := string(raw)
		parts := strings.Fields(text)
		if !strings.HasPrefix(text, "\t") && len(parts) >= 3 && objectHash.MatchString(parts[0]) {
			commit = GitCommit{Hash: parts[0], ShortHash: parts[0][:7]}
			line, _ = strconv.Atoi(parts[2])
			continue
		}
		switch {
		case strings.HasPrefix(text, "author "):
			commit.Author = strings.TrimPrefix(text, "author ")
		case strings.HasPrefix(text, "author-time "):
			seconds, _ := strconv.ParseInt(strings.TrimPrefix(text, "author-time "), 10, 64)
			commit.Time = time.Unix(seconds, 0).UTC()
		case strings.HasPrefix(text, "summary "):
			commit.Subject = strings.TrimPrefix(text, "summary ")
		case strings.HasPrefix(text, "\t"):
			if line > maxBlameLines {
				out.Truncated = true
				return out, nil
			}
			if n := len(out.Ranges); n > 0 && out.Ranges[n-1].Commit.Hash == commit.Hash && out.Ranges[n-1].EndLine+1 == line {
				out.Ranges[n-1].EndLine = line
			} else {
				out.Ranges = append(out.Ranges, GitBlameRange{StartLine: line, EndLine: line, Commit: commit})
			}
		}
	}
	return out, nil
}

// GitReadFile reads an immutable blob without checking it out or changing the
// index. History rows supply the path at that commit, including prior names.
func (s *Service) GitReadFile(roomRef, sessionRef, commit, path string) (filesvc.WorkspaceFile, error) {
	if !objectHash.MatchString(commit) {
		return filesvc.WorkspaceFile{}, errors.New("a full commit hash is required")
	}
	if path == "" {
		return filesvc.WorkspaceFile{}, errors.New("a file path is required")
	}
	if strings.HasPrefix(path, "-") {
		return filesvc.WorkspaceFile{}, errors.New("option-like paths are not allowed")
	}
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	path = filesvc.WorkspacePath(root, path)
	if runtime.IsAccountPath(path) {
		return filesvc.WorkspaceFile{}, errors.New("private account files are not available in the file browser")
	}
	path, err = filepath.Rel(root, path)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	prefix, err := workspaceGitPrefix(ctx, root)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	repoPath := filepath.ToSlash(filepath.Clean(filepath.Join(prefix, path)))
	if repoPath == ".." || strings.HasPrefix(repoPath, "../") {
		return filesvc.WorkspaceFile{}, errors.New("path is outside this repository")
	}
	spec := commit + "^{commit}:" + repoPath
	sizeBytes, cut, err := runWorkspaceGit(ctx, root, 128, "cat-file", "-s", "--", spec)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	if cut {
		return filesvc.WorkspaceFile{}, errors.New("git blob size exceeded output limit")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeBytes)), 10, 64)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	limit := s.MaxBytes
	if limit > filesvc.MaxWorkspaceBytes {
		limit = filesvc.MaxWorkspaceBytes
	}
	data, cut, err := runWorkspaceGitOutput(ctx, root, limit, true, "cat-file", "--", "blob", spec)
	if err != nil {
		return filesvc.WorkspaceFile{}, err
	}
	out, mime := filesvc.PreviewWorkspaceFile(path, data, size, cut)
	if strings.HasPrefix(mime, "image/") && !cut {
		out.URL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return out, nil
}

func workspaceGitPrefix(ctx context.Context, root string) (string, error) {
	data, cut, err := runWorkspaceGit(ctx, root, 4096, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	if cut {
		return "", errors.New("git prefix exceeded output limit")
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}
