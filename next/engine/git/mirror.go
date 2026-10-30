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

	"github.com/resident-labs/dyna/contract"
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
	PushCache(context.Context, string, string, string, []string) error
}

type Mirror struct {
	Data   string
	Token  string
	Runner Runner
	mu     sync.Mutex
}

func (m *Mirror) path(repo contract.RepoName) (string, error) {
	parts := strings.Split(string(repo), "/")
	if len(parts) != 2 || !validPart(parts[0]) || !validPart(parts[1]) {
		return "", errors.New("invalid repository name")
	}
	return filepath.Join(m.Data, "git", parts[0], parts[1]+".git"), nil
}

// Prepare fetches before pushing all branches to the host cache.
func (m *Mirror) Prepare(ctx context.Context, host CacheHost, hostData string, repo contract.RepoName, session contract.SessionID) (contract.BoxRepo, error) {
	if m.Token == "" {
		return contract.BoxRepo{}, MissingTokenError{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return contract.BoxRepo{}, err
	}
	env := m.authEnv()
	remote := "https://github.com/" + string(repo) + ".git"
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		_, err = m.Runner.Run(ctx, env, "clone", "--mirror", remote, path)
	} else if err == nil {
		_, err = m.Runner.Run(ctx, env, "--git-dir", path, "fetch", "origin")
	}
	if err != nil {
		return contract.BoxRepo{}, err
	}
	result, err := m.repo(ctx, path, hostData, repo, session)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	if err := host.PushCache(ctx, path, hostData, string(repo), []string{"+refs/heads/*:refs/heads/*"}); err != nil {
		return contract.BoxRepo{}, err
	}
	return result, nil
}

func (m *Mirror) authEnv() []string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + m.Token))
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + credential, "GIT_TERMINAL_PROMPT=0"}
}

// Ref reads one remote reference without fetching its objects.
func (m *Mirror) Ref(ctx context.Context, repo contract.RepoName, ref string) (string, error) {
	if m.Token == "" {
		return "", MissingTokenError{}
	}
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

// Repo reconstructs the box setup from the mirror after a reconnect.
func (m *Mirror) Repo(ctx context.Context, hostData string, repo contract.RepoName, session contract.SessionID) (contract.BoxRepo, error) {
	path, err := m.path(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	return m.repo(ctx, path, hostData, repo, session)
}

func (m *Mirror) repo(ctx context.Context, path, hostData string, repo contract.RepoName, session contract.SessionID) (contract.BoxRepo, error) {
	out, err := m.Runner.Run(ctx, nil, "--git-dir", path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return contract.BoxRepo{}, err
	}
	return contract.BoxRepo{Name: repo, Cache: filepath.Join(hostData, "git", string(repo)+".git"), Branch: "v2/" + string(session), Base: strings.TrimSpace(string(out))}, nil
}
