package gitsvc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxVisiblePaths = 100
const maxBrowseCommits = 1000

// GitLastCommitsResult omits paths with no commit in the bounded walk.
type GitLastCommitsResult struct {
	Commits   map[string]GitCommit `json:"commits"`
	Truncated bool                 `json:"truncated"`
}

// GitLastCommits reads at most 1000 commits in one process for visible rows.
// Truncated means a missing row can have older history beyond this bound.
func (s *Service) GitLastCommits(roomRef, sessionRef string, paths []string) (GitLastCommitsResult, error) {
	out := GitLastCommitsResult{Commits: map[string]GitCommit{}}
	if len(paths) > maxVisiblePaths {
		return out, fmt.Errorf("at most %d visible paths are allowed", maxVisiblePaths)
	}
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return out, err
	}
	if len(paths) == 0 {
		return out, nil
	}
	root, repoRoot, err := workspaceRepositoryRoot(root)
	if err != nil {
		return out, err
	}
	args := []string{"log", "-1000", "--no-show-signature", "--no-ext-diff", "--no-textconv", "--format=%x00" + gitCommitFormat, "--name-only", "-z", "--no-relative", "--ignore-missing", "HEAD", "--"}
	normalized := make(map[string][]string, len(paths))
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.Rel(repoRoot, path)
		if err != nil {
			return out, err
		}
		key := filepath.ToSlash(filepath.Clean(path))
		normalized[key] = append(normalized[key], paths[i])
		args = append(args, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	data, cut, err := runWorkspaceGit(ctx, repoRoot, s.MaxBytes, args...)
	if err != nil {
		return out, err
	}
	out.Truncated = cut
	fields := bytes.Split(data, []byte{0})
	var commit GitCommit
	count := 0
	firstPath := false
	for i := 0; i < len(fields); i++ {
		field := string(fields[i])
		if field == "" && i+5 < len(fields) && len(fields[i+1]) == 40 {
			commit = parseCommit(fields[i+1 : i+6])
			count++
			firstPath = true
			i += 5
			continue
		}
		if firstPath {
			field = strings.TrimPrefix(field, "\n")
			firstPath = false
		}
		if commit.Hash == "" || field == "" {
			continue
		}
		if cut && i == len(fields)-1 {
			break
		}
		for path, originals := range normalized {
			if path == "." || field == path || strings.HasPrefix(field, path+"/") {
				for _, original := range originals {
					if _, found := out.Commits[original]; !found {
						out.Commits[original] = commit
					}
				}
			}
		}
		if len(out.Commits) == len(paths) {
			break
		}
	}
	out.Truncated = out.Truncated || count == maxBrowseCommits
	return out, nil
}

// workspaceRepositoryRoot finds the worktree boundary without another git
// process. Git still reports invalid repositories through the shared runner.
func workspaceRepositoryRoot(root string) (string, string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return root, dir, nil
		}
		if filepath.Dir(dir) == dir {
			return root, root, nil
		}
	}
}
