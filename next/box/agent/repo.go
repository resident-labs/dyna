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

	"github.com/resident-labs/dyna/box"
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

const (
	setupStarted = "started"
	setupDone    = "done"
)

func setupRepos(ctx context.Context, root, stateRoot string, repos []contract.BoxRepo, name, email string, git gitRunner) error {
	for _, entry := range []struct{ key, value string }{{"user.name", name}, {"user.email", email}} {
		if entry.value != "" {
			if _, err := git(ctx, "", "", "config", "--global", entry.key, entry.value); err != nil {
				return err
			}
		}
	}
	for _, repo := range repos {
		if err := setupRepo(ctx, root, stateRoot, repo, git); err != nil {
			return fmt.Errorf("set up %s: %w", repo.Name, err)
		}
	}
	return nil
}

// setupRepo builds the checkout in root/<repo>. Its state file lives outside the checkout, so Pi and
// git never change it. A checkout marked done is never inspected. A checkout marked started holds only
// setup's own files, because Pi gets no turn before done, so setup empties it first. A checkout with
// no state that already has files is legacy or unknown, so setup marks it done and leaves it as it is.
func setupRepo(ctx context.Context, root, stateRoot string, repo contract.BoxRepo, git gitRunner) error {
	dir := filepath.Join(root, string(repo.Name))
	state := filepath.Join(stateRoot, string(repo.Name))
	value, err := os.ReadFile(state)
	switch {
	case err == nil && string(value) == setupDone:
		return nil
	case err == nil && string(value) == setupStarted:
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	case err == nil:
		return fmt.Errorf("unknown setup state %q", value)
	case !os.IsNotExist(err):
		return err
	default:
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		// LEGACY(v1-transfer): keeping a checkout that has files but no state file, from boxes older than setup state. Remove after the transfer.
		if len(entries) > 0 {
			return writeSetupState(state, setupDone)
		}
	}
	if err := writeSetupState(state, setupStarted); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return err
	}
	steps := box.NewStepLog("repo " + string(repo.Name))
	if _, err := git(ctx, "", "", "clone", "--no-checkout", "--reference", repo.Cache, repo.Cache, dir); err != nil {
		return err
	}
	steps.Done("clone")
	if _, err := git(ctx, dir, "", "remote", "set-url", "origin", "http://127.0.0.1:7781/git/"+string(repo.Name)+".git"); err != nil {
		return err
	}
	base := "refs/remotes/origin/" + repo.Branch
	if _, err := git(ctx, dir, "", "show-ref", "--verify", "--quiet", base); err != nil {
		base = "refs/remotes/origin/" + repo.Base
		if repo.Commit != "" {
			base = repo.Commit
		}
	}
	if _, err := git(ctx, dir, "", "checkout", "-b", repo.Branch, base); err != nil {
		return err
	}
	steps.Done("checkout")
	return writeSetupState(state, setupDone)
}

// writeSetupState replaces the state file with an atomic rename, so a crash leaves the old or the new state.
// The temporary file gets a unique name in the same folder, so no repository name can collide with it.
func writeSetupState(path, value string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(value)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
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
