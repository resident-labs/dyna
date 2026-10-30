package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type gitRunner func(context.Context, string, string, ...string) (string, error)

func runGit(ctx context.Context, dir, index string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if index != "" {
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func setupRepos(ctx context.Context, root string, repos []contract.BoxRepo, git gitRunner) error {
	for _, repo := range repos {
		dir := filepath.Join(root, string(repo.Name))
		if _, err := os.Stat(dir); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
			return err
		}
		tmp := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".setup")
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
		if _, err := git(ctx, "", "", "clone", "--no-checkout", "--reference", repo.Cache, repo.Cache, tmp); err != nil {
			return err
		}
		if _, err := git(ctx, tmp, "", "remote", "set-url", "origin", "http://127.0.0.1:7781/git/"+string(repo.Name)+".git"); err != nil {
			return err
		}
		_, err := git(ctx, tmp, "", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+repo.Branch)
		if err == nil {
			_, err = git(ctx, tmp, "", "checkout", "-b", repo.Branch, "refs/remotes/origin/"+repo.Branch)
		} else {
			_, err = git(ctx, tmp, "", "checkout", "-b", repo.Branch, repo.Base)
		}
		if err != nil {
			return err
		}
		if err := os.Rename(tmp, dir); err != nil {
			return err
		}
	}
	return nil
}

func archiveRepo(ctx context.Context, root string, session contract.SessionID, repo contract.BoxRepo, git gitRunner) (result contract.ArchiveDoneRequest) {
	result.Repo = repo.Name
	dir := filepath.Join(root, string(repo.Name))
	head, err := git(ctx, dir, "", "rev-parse", "HEAD")
	if err == nil {
		var status, remote string
		status, err = git(ctx, dir, "", "status", "--porcelain")
		if err == nil && status == "" {
			remote, err = git(ctx, dir, "", "ls-remote", "origin", "refs/heads/"+repo.Branch)
			if err == nil && strings.Fields(remote) != nil && strings.Fields(remote)[0] == head {
				result.Commit, result.Skipped = head, true
				return
			}
		}
	}
	if err == nil {
		var f *os.File
		f, err = os.CreateTemp("", "dyna-index-")
		if err == nil {
			index := f.Name()
			err = f.Close()
			defer os.Remove(index)
			if err == nil {
				err = os.Remove(index)
			}
			if err == nil {
				_, err = git(ctx, dir, index, "read-tree", "HEAD")
			}
			if err == nil {
				_, err = git(ctx, dir, index, "add", "-A")
			}
			var tree string
			if err == nil {
				tree, err = git(ctx, dir, index, "write-tree")
			}
			if err == nil {
				result.Commit, err = git(ctx, dir, index, "commit-tree", tree, "-p", head, "-m", "archive "+string(session))
			}
			if err == nil {
				_, err = git(ctx, dir, "", "push", "origin", result.Commit+":refs/dyna/archive/"+string(session))
			}
		}
	}
	if err != nil {
		result.Commit = ""
		result.Error = err.Error()
	}
	return
}
