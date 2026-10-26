// Package ghclient runs GitHub CLI calls with repository-scoped credentials.
package ghclient

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/processguard"
)

// Command disables interactive behavior and inherited Git repository overrides.
func Command(ctx context.Context, binary, dir string, args ...string) *exec.Cmd {
	if binary == "" {
		binary = executable(ctx)
	}
	cmd := processguard.Command(ctx, binary, args...)
	cmd.Dir = dir

	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "GH_REPO=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_PAGER=cat", "NO_COLOR=1", "CLICOLOR=0", "GIT_TERMINAL_PROMPT=0")
	return childenv.Clean(cmd)
}

// Run executes a configured command.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	return RunWithAccount(ctx, cmd, nil)
}

// RunWithAccount checks the selected identity before executing the API request.
func RunWithAccount(ctx context.Context, cmd *exec.Cmd, before func(string) error) error {
	r := repositoryFor(ctx, cmd)
	if err := checkPolicy(ctx, cmd.Dir, r, cmd.Args[1:]); err != nil {
		// A REST read of a public repository outside every room needs no room
		// and no account. It runs with no credentials at all, so it can read
		// only what anyone can. Writes and every other command take no part.
		if _, ok := publicAPIRead(ctx, r, cmd.Args[1:]); ok {
			return runAnonymously(cmd)
		}
		return err
	}
	token, login := "", ""
	if r.name != "" {
		var err error
		login, token, err = selectAccount(ctx, cmd, r)
		if err != nil {
			return err
		}
	}
	if before != nil {
		if err := before(login); err != nil {
			return err
		}
	}
	return runWithToken(cmd, token)
}

// runAnonymously runs gh with no token and an empty configuration directory, so
// neither the environment nor a saved login supplies credentials.
func runAnonymously(cmd *exec.Cmd) error {
	dir, err := os.MkdirTemp("", "dyna-gh-anonymous-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cmd.Env = append(withoutTokens(cmd.Env), "GH_CONFIG_DIR="+dir)
	return cmd.Run()
}

func runWithToken(cmd *exec.Cmd, token string) error {
	if token != "" {
		cmd.Env = append(withoutTokens(cmd.Env), "GH_TOKEN="+token, "GH_ENTERPRISE_TOKEN="+token)
	}
	if token == "" || cmd.Stderr == nil {
		return cmd.Run()
	}
	destination := cmd.Stderr
	stderr := boundedio.New(8192)
	cmd.Stderr = stderr
	err := cmd.Run()
	_, _ = io.WriteString(destination, strings.ReplaceAll(string(stderr.Bytes()), token, "[redacted]"))
	return err
}
