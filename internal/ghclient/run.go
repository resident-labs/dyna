// Package ghclient runs GitHub CLI calls with repository-scoped credentials.
package ghclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"dyna-agent/internal/approvals"
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

type approvedClassKey struct{}

// WithApprovedClass limits execution to the class that approved the write.
func WithApprovedClass(ctx context.Context, class approvals.Class) context.Context {
	return context.WithValue(ctx, approvedClassKey{}, class)
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
		return outsideReadError(r, cmd.Args[1:], err)
	}
	media, err := stripMedia(ctx, cmd)
	if err != nil {
		return err
	}
	cleanup, err := ComposeBody(ctx, cmd, r)
	if err != nil {
		return err
	}
	defer cleanup()
	token, login := "", ""
	if r.name != "" {
		var err error
		login, token, err = selectAccountAfterPolicy(ctx, cmd, r)
		if err != nil {
			return err
		}
		if !isRead(cmd.Args[1:]) {
			_, decision, err := decideCommand(ctx, cmd, r, login, nil)
			if err != nil {
				return err
			}
			if approved, ok := ctx.Value(approvedClassKey{}).(approvals.Class); ok && approved != approvals.ClassOwner && decision.Class == approvals.ClassOwner {
				return fmt.Errorf("GitHub write now requires owner approval under rule %s; submit it again", decision.Rule)
			}
		}
	}
	if before != nil {
		if err := before(login); err != nil {
			return err
		}
	}
	mediaCleanup, err := uploadMedia(ctx, cmd, r, token, media)
	if err != nil {
		return err
	}
	defer mediaCleanup()
	err = runWithToken(cmd, token)
	if err != nil && len(media) > 0 {
		return fmt.Errorf("PR write failed after media assets were uploaded or reused: %w", err)
	}
	return err
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
