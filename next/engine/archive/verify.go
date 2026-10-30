// Package archive verifies reports from closing boxes.
package archive

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}
type GitRunner struct{}

func (GitRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}

type Verifier struct {
	Data   string
	Token  string
	Runner Runner
}

func (v *Verifier) Verify(ctx context.Context, session contract.SessionID, report contract.ArchiveDoneRequest) error {
	if v.Token == "" {
		return errors.New("GH_TOKEN is not set")
	}
	parts := strings.Split(string(report.Repo), "/")
	if len(parts) != 2 {
		return errors.New("invalid repository name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") != "" {
			return errors.New("invalid repository name")
		}
	}
	dir, err := os.MkdirTemp(v.Data, "archive-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := v.Runner.Run(ctx, nil, "init", "--bare", dir); err != nil {
		return err
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + v.Token))
	env := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + credential, "GIT_TERMINAL_PROMPT=0"}
	ref := "refs/dyna/archive/" + string(session)
	if report.Skipped {
		ref = "refs/heads/v2/" + string(session)
	}
	if _, err := v.Runner.Run(ctx, env, "--git-dir", dir, "fetch", "--no-tags", "https://github.com/"+string(report.Repo)+".git", ref); err != nil {
		return err
	}
	hash, err := v.Runner.Run(ctx, nil, "--git-dir", dir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(hash)) != report.Commit {
		return errors.New("archive ref mismatch")
	}
	return nil
}
