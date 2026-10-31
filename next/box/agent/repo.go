package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

func setupRepos(ctx context.Context, root string, repos []contract.BoxRepo, name, email string, git gitRunner) error {
	for _, entry := range []struct{ key, value string }{{"user.name", name}, {"user.email", email}} {
		if entry.value != "" {
			if _, err := git(ctx, "", "", "config", "--global", entry.key, entry.value); err != nil {
				return err
			}
		}
	}
	for _, repo := range repos {
		dir := filepath.Join(root, string(repo.Name))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			continue
		}
		if err := setupRepo(ctx, dir, repo, git); err != nil {
			return err
		}
	}
	return nil
}

// setupRepo keeps the checkout directory itself in place for a running Pi process.
func setupRepo(ctx context.Context, dir string, repo contract.BoxRepo, git gitRunner) (err error) {
	defer func() {
		if err == nil {
			return
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}()
	if _, err = git(ctx, "", "", "clone", "--no-checkout", "--reference", repo.Cache, repo.Cache, dir); err != nil {
		return err
	}
	if _, err = git(ctx, dir, "", "remote", "set-url", "origin", "http://127.0.0.1:7781/git/"+string(repo.Name)+".git"); err != nil {
		return err
	}
	_, err = git(ctx, dir, "", "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+repo.Branch)
	base := "refs/remotes/origin/" + repo.Branch
	if err != nil {
		base = "refs/remotes/origin/" + repo.Base
	}
	_, err = git(ctx, dir, "", "checkout", "-b", repo.Branch, base)
	return err
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
			fields := strings.Fields(remote)
			if err == nil && len(fields) > 0 && fields[0] == head {
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
				result.Commit, err = git(ctx, dir, index, "-c", "user.name=Dyna Archive", "-c", "user.email=archive@dyna.invalid", "commit-tree", tree, "-p", head, "-m", "archive "+string(session))
			}
			if err == nil {
				_, err = git(ctx, dir, "", "push", "--force", "origin", result.Commit+":refs/dyna/archive/"+string(session))
			}
		}
	}
	if err != nil {
		result.Commit = ""
		result.Error = err.Error()
	}
	return
}

func archiveAndReport(ctx context.Context, root string, session contract.SessionID, repos []contract.BoxRepo, git gitRunner, after func(time.Duration) <-chan time.Time, done func(context.Context, contract.ArchiveDoneRequest) error) error {
	if len(repos) == 0 {
		return done(ctx, contract.ArchiveDoneRequest{})
	}
	for _, repo := range repos {
		var result contract.ArchiveDoneRequest
		attempts := 0
		if err := retryClose(ctx, after, func(ctx context.Context) error {
			result = archiveRepo(ctx, root, session, repo, git)
			attempts++
			if result.Error != "" && attempts < 5 {
				return errors.New(result.Error)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := done(ctx, result); err != nil {
			return err
		}
	}
	return nil
}
