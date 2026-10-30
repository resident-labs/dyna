package git

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Runner executes git without putting credentials in command arguments.
type Runner interface {
	Run(context.Context, []string, ...string) error
}

// GitRunner runs the system git command.
type GitRunner struct{}

func (GitRunner) Run(ctx context.Context, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

// CacheHost pushes a local mirror to the bare cache on a host.
type CacheHost interface {
	PushCache(context.Context, string, string, string, []string) error
}

type Mirror struct {
	Data   string
	Token  string
	Runner Runner
}

// Prepare fetches before pushing the requested refs into the host cache.
func (m Mirror) Prepare(ctx context.Context, host CacheHost, hostData string, repo contract.RepoName, refs []string) (string, error) {
	parts := strings.Split(string(repo), "/")
	if len(parts) != 2 || !validPart(parts[0]) || !validPart(parts[1]) {
		return "", errors.New("invalid repository name")
	}
	path := filepath.Join(m.Data, "git", parts[0], parts[1]+".git")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := m.Runner.Run(ctx, nil, "init", "--bare", path); err != nil {
		return "", err
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + m.Token))
	env := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + credential, "GIT_TERMINAL_PROMPT=0"}
	if err := m.Runner.Run(ctx, env, "--git-dir", path, "fetch", "--no-tags", "https://github.com/"+string(repo)+".git", "+refs/heads/*:refs/heads/*"); err != nil {
		return "", err
	}
	if err := host.PushCache(ctx, path, hostData, string(repo), refs); err != nil {
		return "", err
	}
	return filepath.Join(hostData, "git", parts[0], parts[1]+".git"), nil
}
