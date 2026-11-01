package git

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/images"
)

// Runner executes git without putting credentials in command arguments.
type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

type MissingTokenError struct{}

func (MissingTokenError) Error() string { return "GH_TOKEN is not set" }

type GitRunner struct{}

func (GitRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}

type CacheHost interface {
	PushCache(context.Context, string, string, []string) error
}

type Mirror struct {
	Layout box.Layout
	Token  string
	Runner Runner
	mu     sync.Mutex
}

const SessionBranchPrefix = "v2/"

// Prepare optionally fetches and pushes branches before returning the box setup.
func (m *Mirror) Prepare(ctx context.Context, host CacheHost, repo contract.RepoName, session contract.SessionID, fetch bool) (contract.BoxRepo, error) {
	if fetch && m.Token == "" {
		return contract.BoxRepo{}, MissingTokenError{}
	}
	steps := box.NewStepLog("repo " + string(repo))
	m.mu.Lock()
	defer m.mu.Unlock()
	steps.Done("mirror wait")
	path, err := m.Layout.Git(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	cache, err := m.Layout.HostGit(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	if fetch {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return contract.BoxRepo{}, err
		}
		env := m.authEnv()
		remote := "https://github.com/" + string(repo) + ".git"
		_, err = os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			_, err = m.Runner.Run(ctx, env, "clone", "--bare", "--no-tags", remote, path)
		} else if err == nil {
			_, err = m.Runner.Run(ctx, env, "--git-dir", path, "fetch", "--prune", "--no-tags", "origin", "+refs/heads/*:refs/heads/*")
			if err != nil {
				return contract.BoxRepo{}, err
			}
			out, err := m.Runner.Run(ctx, env, "--git-dir", path, "ls-remote", "--symref", "origin", "HEAD")
			if err != nil {
				return contract.BoxRepo{}, err
			}
			fields := strings.Fields(string(out))
			if len(fields) < 3 || fields[0] != "ref:" || !strings.HasPrefix(fields[1], "refs/heads/") || fields[2] != "HEAD" {
				return contract.BoxRepo{}, errors.New("remote default branch not found")
			}
			_, err = m.Runner.Run(ctx, nil, "--git-dir", path, "symbolic-ref", "HEAD", fields[1])
			if err != nil {
				return contract.BoxRepo{}, err
			}
		}
		if err != nil {
			return contract.BoxRepo{}, err
		}
		steps.Done("fetch")
		if err := host.PushCache(ctx, path, cache, []string{"+refs/heads/*:refs/heads/*"}); err != nil {
			return contract.BoxRepo{}, err
		}
		steps.Done("cache push")
	}
	out, err := m.Runner.Run(ctx, nil, "--git-dir", path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return contract.BoxRepo{}, err
	}
	return contract.BoxRepo{Name: repo, Cache: cache, Branch: SessionBranchPrefix + string(session), Base: strings.TrimSpace(string(out))}, nil
}

func (m *Mirror) authEnv() []string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + m.Token))
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + credential, "GIT_TERMINAL_PROMPT=0"}
}

// ImageContext reads the default-branch toolchain context from the mirror.
func (m *Mirror) ImageContext(ctx context.Context, repo contract.RepoName) (string, images.TreeID, []byte, []box.Need, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.Layout.Git(repo)
	if err != nil {
		return "", "", nil, nil, err
	}
	return images.Context(ctx, m.Runner, path)
}

// Size reads the repository's default box size from the mirror. A missing mirror has none.
func (m *Mirror) Size(ctx context.Context, repo contract.RepoName) (contract.BoxSizeName, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.Layout.Git(repo)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}
	size, err := images.DefaultSize(ctx, m.Runner, path)
	return contract.BoxSizeName(size), err
}

// DeleteArchiveRef deletes only the recorded archive commit, never a changed ref.
func (m *Mirror) DeleteArchiveRef(ctx context.Context, repo contract.RepoName, ref, commit string) error {
	if m.Token == "" {
		return MissingTokenError{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.Layout.Git(repo)
	if err != nil {
		return err
	}
	remote := "https://github.com/" + string(repo) + ".git"
	out, err := m.Runner.Run(ctx, m.authEnv(), "ls-remote", remote, ref)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return nil
	}
	if len(fields) != 2 || fields[0] != commit || fields[1] != ref {
		return errors.New("archive ref changed")
	}
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if _, err := m.Runner.Run(ctx, nil, "init", "--bare", path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, m.authEnv(), "--git-dir", path, "push", "--force-with-lease="+ref+":"+commit, remote, ":"+ref)
	return err
}

// Ref reads one remote reference without fetching its objects.
func (m *Mirror) Ref(ctx context.Context, repo contract.RepoName, ref string) (string, error) {
	out, err := m.Runner.Run(ctx, m.authEnv(), "ls-remote", "https://github.com/"+string(repo)+".git", ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != ref {
		return "", errors.New("remote ref not found")
	}
	return fields[0], nil
}
