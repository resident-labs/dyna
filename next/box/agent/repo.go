package agent

import (
	"context"
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

// runGit runs Dyna's own git operation in a repository. Dyna's backups and bookkeeping never run the repository's hooks,
// whatever its hooks folder or core.hooksPath holds, because those guard the owner's and the agent's commands, not Dyna's.
// A command line -c beats the repository's config, and --no-verify would not reach checkout, merge or worktree hooks.
func runGit(ctx context.Context, dir, index string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second // a killed git's helpers can hold the output open
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
// setup's own files, because Pi gets no turn before done, so setup empties it first.
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
