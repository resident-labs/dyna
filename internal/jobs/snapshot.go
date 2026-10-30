package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotGitTree identifies a directory outside a Git worktree.
var ErrNotGitTree = errors.New("not a git worktree")

// SnapshotTree writes a deterministic commit of the current worktree without
// changing its index or refs. The returned object is not protected from GC.
func SnapshotTree(ctx context.Context, dir string) (string, string, string, error) {
	run := func(env []string, args ...string) (string, error) {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), env...)
		b, err := c.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	root, err := run(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %v", ErrNotGitTree, err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", "", err
	}
	subdir, err := filepath.Rel(root, dir)
	if err != nil {
		return "", "", "", err
	}
	index, err := run(nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	data, err := os.ReadFile(index)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", "", err
	}
	tmp, err := os.CreateTemp("", "dyna-index-")
	if err != nil {
		return "", "", "", err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Close(); err != nil {
		return "", "", "", err
	}
	if data == nil {
		if err = os.Remove(tmp.Name()); err != nil {
			return "", "", "", err
		}
	} else if err = os.WriteFile(tmp.Name(), data, 0600); err != nil {
		return "", "", "", err
	}
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if _, err = run(env, "-C", root, "add", "-A", "--", "."); err != nil {
		return "", "", "", fmt.Errorf("snapshot add: %w", err)
	}
	tree, err := run(env, "write-tree")
	if err != nil {
		return "", "", "", err
	}
	head, err := run(nil, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", err
	}
	headTree, err := run(nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", "", "", err
	}
	if tree == headTree {
		return root, head, subdir, nil
	}
	fields, err := run(nil, "show", "-s", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI", "HEAD")
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(fields, "\x00")
	if len(parts) != 6 {
		return "", "", "", errors.New("invalid HEAD identity")
	}
	env = append(env, "GIT_AUTHOR_NAME="+parts[0], "GIT_AUTHOR_EMAIL="+parts[1], "GIT_AUTHOR_DATE="+parts[2], "GIT_COMMITTER_NAME="+parts[3], "GIT_COMMITTER_EMAIL="+parts[4], "GIT_COMMITTER_DATE="+parts[5])
	sha, err := run(env, "commit-tree", tree, "-p", head, "-m", "Dyna tree snapshot")
	return root, sha, subdir, err
}
