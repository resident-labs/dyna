package worktrees

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/repoid"
)

// gitOriginOutput gives GitHub HTTPS reads the same account and repository
// checks as gh. Other hosts and transports keep their native credentials.
func gitOriginOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	remote, err := repoid.Git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	identity, err := repoid.RemoteIdentity(dir, remote)
	if err != nil {
		return nil, err
	}
	host, _, hosted := strings.Cut(identity, "/")
	if !hosted || host != "github.com" || !strings.HasPrefix(remote, "https://") {
		return gitOutput(ctx, dir, args...)
	}
	origin, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}
	// Git matches credential paths case-sensitively. Keep the URL's path;
	// ghclient compares repository identities without regard to case.
	name := strings.TrimSuffix(strings.Trim(origin.Path, "/"), ".git")
	ctx = ghclient.WithRepository(ctx, host, name)
	cmd := gitexec.Command(ctx, dir, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = ghclient.RunGit(ctx, "", cmd)
	return out.Bytes(), err
}
